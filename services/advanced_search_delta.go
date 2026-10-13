package services

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"pvfine/internal/pvf"
)

var errAdvancedPoolChanged = errors.New("字符串池偏移代际不兼容")

func (d *advancedSQLite) requestContext() context.Context {
	if d.requestCtx != nil {
		return d.requestCtx
	}
	return d.ctx
}

func (d *advancedSQLite) captureBaseline(c *core, a *pvf.Archive) error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.advancedDisk != d || c.archive != a || d.ctx.Err() != nil {
		return context.Canceled
	}
	d.entries = archiveEntryIndexes(a)
	d.poolState = a.StringPoolState()
	d.indexedEpoch = d.epoch
	d.published.Store(true)
	return nil
}

// Only the metadata/pool database becomes writable. Immutable, potentially
// gigabyte-sized reference shards and cache hard links are never modified.
func (d *advancedSQLite) prepareOverlay() error {
	if d.mutable {
		return nil
	}
	ctx := d.requestContext()
	tmp, err := os.CreateTemp(d.dir, ".delta-*.db")
	if err != nil {
		return err
	}
	path := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(path)
	if err = backupSQLiteDatabase(ctx, d.db, path); err != nil {
		return err
	}
	db, err := openArchiveIndexDatabase(path, false)
	if err != nil {
		return err
	}
	if err = configureArchiveIndex(db, false); err != nil {
		_ = db.Close()
		return err
	}
	if !d.overlay {
		_, err = db.ExecContext(ctx, `CREATE TABLE file_map(orig_index INTEGER PRIMARY KEY,file_index INTEGER UNIQUE,overridden INTEGER NOT NULL DEFAULT 0);
INSERT INTO file_map(orig_index,file_index) SELECT file_index,file_index FROM files;
CREATE TABLE delta_refs(file_index INTEGER,offset INTEGER,occurrences INTEGER,types INTEGER,fields INTEGER,PRIMARY KEY(file_index,offset)) WITHOUT ROWID;
CREATE INDEX delta_refs_offset ON delta_refs(offset,file_index)`)
	}
	_ = db.Close()
	if err != nil {
		return err
	}
	if err = d.db.Close(); err != nil {
		return err
	}
	mainPath := filepath.Join(d.dir, "index.db")
	if err = os.Rename(path, mainPath); err != nil {
		d.db, _ = openAdvancedReadDB(mainPath)
		return err
	}
	d.overlay = true
	d.db, err = openArchiveIndexDatabase(mainPath, false)
	if err == nil {
		err = configureArchiveIndex(d.db, false)
	}
	if err == nil {
		err = enableArchiveIndexWAL(d.db)
	}
	if err != nil {
		return err
	}
	d.overlay, d.mutable, d.cachePath = true, true, ""
	return nil
}

type advancedDeltaFile struct {
	index int32
	path  string
	file  pvf.File
	refs  []pvf.StringReference
}

func (d *advancedSQLite) syncDelta(c *core, a *pvf.Archive, epoch uint64) error {
	if d.db == nil {
		return errors.New("高级搜索投影连接不可用")
	}
	ctx := d.requestContext()
	c.mu.RLock()
	if c.advancedDisk != d || c.archive != a || d.epoch != epoch || ctx.Err() != nil {
		c.mu.RUnlock()
		return ErrAdvancedCursorStale
	}
	revision := c.batchRevision
	ids := make(map[uint64]struct{}, len(d.pendingIDs))
	for id := range d.pendingIDs {
		ids[id] = struct{}{}
	}
	snapshot := a.CloneForBatch()
	c.mu.RUnlock()
	if !snapshot.ExtendsStringPools(d.poolState) {
		return errAdvancedPoolChanged
	}
	next := archiveEntryIndexes(snapshot)
	oldByID := make(map[uint64]int32, len(d.entries))
	for index, id := range d.entries {
		oldByID[id] = int32(index)
	}
	retained := make([]bool, len(d.entries))
	moved := make(map[int32]int32)
	var changedIndexes []int32
	for index, id := range next {
		oldIndex, exists := oldByID[id]
		if exists {
			retained[oldIndex] = true
			if oldIndex != int32(index) {
				moved[oldIndex] = int32(index)
			}
		}
		_, changed := ids[id]
		if !exists || changed {
			changedIndexes = append(changedIndexes, int32(index))
		}
	}
	var changes []advancedDeltaFile
	scanner := snapshot.NewSelectedStringReferenceScanner(changedIndexes)
	for {
		index, refs, ok, err := scanner.Next(ctx)
		if err != nil {
			return err
		}
		if !ok {
			break
		}
		changes = append(changes, advancedDeltaFile{index, snapshot.Path(index), snapshot.File(index), refs})
	}
	var appended []pvf.StringPoolEntry
	pools := snapshot.NewAppendedStringPoolScanner(d.poolState)
	for {
		entry, ok, err := pools.Next(ctx)
		if err != nil {
			return err
		}
		if !ok {
			break
		}
		appended = append(appended, entry)
	}
	// Resolve only existing placeholder strings, not every pool value.
	resolved := make(map[int32]string)
	rows, err := d.db.QueryContext(ctx, `SELECT offset,value FROM pool WHERE instr(value,'<')>0 AND instr(value,'::')>0`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var offset int32
		var value string
		if err = rows.Scan(&offset, &value); err != nil {
			break
		}
		text := snapshot.ResolvePlaceholders(value)
		if text == value {
			text = ""
		}
		resolved[offset] = text
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return err
	}
	state := snapshot.StringPoolState()
	if err = d.prepareOverlay(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.advancedDisk != d || c.archive != a || d.epoch != epoch || c.batchRevision != revision || ctx.Err() != nil {
		return ErrAdvancedCursorStale
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `CREATE TEMP TABLE advance_map(old_index INTEGER PRIMARY KEY,new_index INTEGER)`); err != nil {
		return err
	}
	mapStmt, err := tx.PrepareContext(ctx, `INSERT INTO advance_map VALUES(?,?)`)
	if err != nil {
		return err
	}
	defer mapStmt.Close()
	for oldIndex, newIndex := range moved {
		if _, err = mapStmt.ExecContext(ctx, oldIndex, newIndex); err != nil {
			return err
		}
	}
	for index, keep := range retained {
		if keep {
			continue
		}
		for _, table := range []string{"files", "file_map", "delta_refs"} {
			if _, err = tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE file_index=?`, index); err != nil {
				return err
			}
		}
	}
	for _, table := range []string{"files", "file_map", "delta_refs"} {
		if _, err = tx.ExecContext(ctx, `UPDATE `+table+` SET file_index=-file_index-1 WHERE file_index IN (SELECT old_index FROM advance_map)`); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE `+table+` SET file_index=(SELECT new_index FROM advance_map WHERE old_index=-file_index-1) WHERE file_index<0`); err != nil {
			return err
		}
	}
	refStmt, err := tx.PrepareContext(ctx, `INSERT INTO delta_refs VALUES(?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer refStmt.Close()
	for _, change := range changes {
		if _, err = tx.ExecContext(ctx, `INSERT INTO files VALUES(?,?,?,?,?) ON CONFLICT(file_index) DO UPDATE SET path=excluded.path,lower_path=excluded.lower_path,size=excluded.size,data_type=excluded.data_type`, change.index, change.path, strings.ToLower(change.path), change.file.DataSize, change.file.DataType); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE file_map SET overridden=1 WHERE file_index=?`, change.index); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM delta_refs WHERE file_index=?`, change.index); err != nil {
			return err
		}
		for _, ref := range change.refs {
			types, fields := 0, 0
			for n, token := range ref.TokenTypes {
				types |= int(token) << (n * 4)
			}
			for _, field := range ref.FileFields {
				if field == "name" {
					fields |= 1
				}
				if field == "path" {
					fields |= 2
				}
			}
			if _, err = refStmt.ExecContext(ctx, change.index, ref.Offset, ref.Occurrences, types, fields); err != nil {
				return err
			}
		}
	}
	for _, entry := range appended {
		text := snapshot.ResolvePlaceholders(entry.Value)
		if text == entry.Value {
			text = ""
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO pool VALUES(?,?,?,?)`, entry.Offset, entry.Pool, entry.Value, text); err != nil {
			return err
		}
	}
	for offset, text := range resolved {
		if _, err = tx.ExecContext(ctx, `UPDATE pool SET resolved=? WHERE offset=?`, text, offset); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `DROP TABLE advance_map`); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	for _, q := range d.queries {
		_ = os.Remove(q.path)
	}
	d.queries = make(map[uint64]*advancedDiskQuery)
	d.entries, d.poolState, d.indexedEpoch, d.pendingIDs = next, state, epoch, nil
	c.advancedStatus = AdvancedSearchIndexStatus{State: AdvancedIndexStateReady, Stage: "ready-delta", Done: len(changes), Total: int(snapshot.FileCount())}
	developmentLog("[pvfine:advanced] sqlite delta files=%d remapped=%d appended_strings=%d", len(changes), len(moved), len(appended))
	return nil
}
