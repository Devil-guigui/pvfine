package services

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"pvfine/internal/pvf"
)

func archiveEntryIndexes(a *pvf.Archive) []uint64 {
	entries := make([]uint64, a.FileCount())
	for index := range entries {
		entries[index] = a.FileIdentity(int32(index))
	}
	return entries
}

func (c *core) failSQLiteProjectionLocked(err error) {
	index := c.diskIndex
	if index == nil {
		return
	}
	index.dbMu.Lock()
	if index.db != nil {
		_ = index.db.Close()
	}
	index.dbMu.Unlock()
	index.entries = nil
	if c.indexCancel != nil {
		c.indexCancel()
		c.indexCancel = nil
	}
	c.indexGen++
	c.indexStatus = IndexStatus{State: IndexStateError, Stage: "sqlite-delta", Error: err.Error()}
}

// Every mutation entry point calls the scheduler before returning success.
// Refresh basic file metadata even for unregistered payloads; later semantic
// transactions cannot be the first place where size/type becomes correct.
func (c *core) syncSQLiteMutationMetadataLocked(a *pvf.Archive, summary pvf.MutationSummary) error {
	index := c.diskIndex
	if index == nil {
		return nil
	}
	if index.entries == nil {
		return errors.New("SQLite 文件投影需要修复")
	}
	if summary.Structural {
		return nil
	} // already committed by applyArchiveDelta
	ids := make(map[uint64]struct{})
	changed := make(map[int32]struct{})
	for _, mutation := range summary.Files {
		if mutation.Kind == pvf.MutationRemoved {
			continue
		}
		if mutation.Identity != 0 {
			ids[mutation.Identity] = struct{}{}
		} else if fileIndex, exists := a.Find(mutation.Path); exists {
			changed[fileIndex] = struct{}{}
		}
	}
	for fileIndex, id := range index.entries {
		if _, exists := ids[id]; exists {
			changed[int32(fileIndex)] = struct{}{}
		}
	}
	if err := index.refreshFileMetadata(a, changed); err != nil {
		c.failSQLiteProjectionLocked(err)
		return err
	}
	return nil
}

// detachCacheLocked makes the first write private to this workspace. Further
// mutations reuse this connection; they never copy the complete database.
// Caller holds core.mu followed by dbMu. The cache remains immutable.
func (i *sqliteArchiveIndex) detachCacheLocked() error {
	if i.tempDir != "" || i.path == "" {
		return nil
	}
	dir, err := os.MkdirTemp("", "pvfine-index-session-")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "index.db")
	if err = backupSQLiteDatabase(context.Background(), i.db, path); err != nil {
		_ = os.RemoveAll(dir)
		return err
	}
	db, err := openArchiveIndexDatabase(path, false)
	if err == nil {
		err = configureArchiveIndex(db, false)
	}
	if err == nil {
		err = enableArchiveIndexWAL(db)
	}
	if err != nil {
		if db != nil {
			_ = db.Close()
		}
		_ = os.RemoveAll(dir)
		return err
	}
	old := i.db
	i.db, i.path, i.tempDir = db, path, dir
	_ = old.Close()
	return nil
}

// applyArchiveDelta changes the file projection atomically, preserving existing
// SQL indexes and semantic rows. Stable entry identities disambiguate duplicate
// paths and survive any number of compacting removals in a compound mutation.
// Caller holds core.mu, so no service reader observes a mixed archive/projection.
func (i *sqliteArchiveIndex) applyArchiveDelta(a *pvf.Archive) error {
	i.dbMu.Lock()
	defer i.dbMu.Unlock()
	if i.db == nil || i.entries == nil {
		return errors.New("SQLite 文件投影缺少条目身份")
	}
	if err := i.detachCacheLocked(); err != nil {
		return err
	}
	next := archiveEntryIndexes(a)
	// Usually the prefix is unchanged (append/overwrite). Only construct a
	// lookup map when compaction or archive replacement actually changed it.
	var oldByID map[uint64]int32
	for index, id := range next {
		if index < len(i.entries) && i.entries[index] != id {
			oldByID = make(map[uint64]int32, len(i.entries))
			for oldIndex, oldID := range i.entries {
				oldByID[oldID] = int32(oldIndex)
			}
			break
		}
	}
	changedEntries := make(map[uint64]struct{})
	for _, mutation := range a.MutationsSince(0).Files {
		changedEntries[mutation.Identity] = struct{}{}
	}
	tx, err := i.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`CREATE TEMP TABLE delta_map(old_index INTEGER PRIMARY KEY,new_index INTEGER); CREATE TEMP TABLE delta_dirs(path TEXT PRIMARY KEY)`); err != nil {
		return err
	}
	// Creation and removal are in this transaction: rollback also removes the
	// temporary tables, so retries cannot inherit a previous delta.
	mapStmt, err := tx.Prepare(`INSERT INTO delta_map VALUES(?,?)`)
	if err != nil {
		return err
	}
	defer mapStmt.Close()
	retained := make([]bool, len(i.entries))
	touched := make([]int32, 0, len(changedEntries))
	for index, id := range next {
		oldIndex, exists := int32(index), index < len(i.entries) && i.entries[index] == id
		if oldByID != nil {
			oldIndex, exists = oldByID[id]
		}
		if exists {
			retained[oldIndex] = true
			if oldIndex != int32(index) {
				if _, err = mapStmt.Exec(oldIndex, index); err != nil {
					return err
				}
			}
		}
		if !exists {
			touched = append(touched, int32(index))
		} else if _, changed := changedEntries[id]; changed {
			touched = append(touched, int32(index))
		}
	}
	for index, keep := range retained {
		if keep {
			continue
		}
		if _, err = tx.Exec(`INSERT OR IGNORE INTO delta_dirs SELECT parent FROM files WHERE file_index=?`, index); err != nil {
			return err
		}
		for _, table := range []string{"records", "tags", "visuals", "semantic_dependencies", "files"} {
			if _, err = tx.Exec(`DELETE FROM `+table+` WHERE file_index=?`, index); err != nil {
				return err
			}
		}
	}
	// Two phases avoid primary-key collisions regardless of update order.
	for _, table := range []string{"files", "records", "tags", "visuals", "semantic_dependencies"} {
		if _, err = tx.Exec(`UPDATE ` + table + ` SET file_index=-file_index-1 WHERE file_index IN (SELECT old_index FROM delta_map)`); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE ` + table + ` SET file_index=(SELECT new_index FROM delta_map WHERE old_index=-file_index-1) WHERE file_index<0`); err != nil {
			return err
		}
	}
	for _, index := range touched {
		path := a.Path(index)
		if path == "" {
			continue
		}
		file := a.File(index)
		parent, name := splitParent(path)
		if _, err = tx.Exec(`INSERT INTO files VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(file_index) DO UPDATE SET path=excluded.path,lower_path=excluded.lower_path,name=excluded.name,lower_name=excluded.lower_name,parent=excluded.parent,size=excluded.size,data_type=excluded.data_type,change_kind=excluded.change_kind`, index, path, strings.ToLower(path), name, strings.ToLower(name), parent, file.DataSize, file.DataType, archiveChangeKind(a, index)); err != nil {
			return err
		}
		for dir := parent; ; {
			if _, err = tx.Exec(`INSERT OR IGNORE INTO delta_dirs VALUES(?)`, dir); err != nil {
				return err
			}
			if dir == "" {
				break
			}
			upper, dirName := splitParent(dir)
			if _, err = tx.Exec(`INSERT OR IGNORE INTO dirs(path,parent,name) VALUES(?,?,?)`, dir, upper, dirName); err != nil {
				return err
			}
			dir = upper
		}
		if _, err = tx.Exec(`UPDATE records SET size=?,data_type=? WHERE file_index=?`, file.DataSize, file.DataType, index); err != nil {
			return err
		}
	}
	if err = refreshDeltaDirectories(tx); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE meta SET value='0' WHERE key='complete'; DROP TABLE delta_map; DROP TABLE delta_dirs`); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	i.entries = next
	i.queueSemanticMutations(a.MutationsSince(0))
	i.ready, i.dirty = false, true
	return nil
}

func refreshDeltaDirectories(tx *sql.Tx) error {
	rows, err := tx.Query(`SELECT path FROM delta_dirs`)
	if err != nil {
		return err
	}
	dirs := make(map[string]struct{})
	for rows.Next() {
		var path string
		if err = rows.Scan(&path); err != nil {
			_ = rows.Close()
			return err
		}
		for {
			dirs[path] = struct{}{}
			if path == "" {
				break
			}
			path, _ = splitParent(path)
		}
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	ordered := make([]string, 0, len(dirs))
	for dir := range dirs {
		ordered = append(ordered, dir)
	}
	sort.Slice(ordered, func(x, y int) bool { return len(ordered[x]) > len(ordered[y]) })
	for _, dir := range ordered {
		if _, err = tx.Exec(`UPDATE dirs SET child_count=(SELECT count(*) FROM files WHERE parent=?)+(SELECT count(*) FROM dirs WHERE parent=? AND path<>'') WHERE path=?`, dir, dir, dir); err != nil {
			return err
		}
		if dir != "" {
			if _, err = tx.Exec(`DELETE FROM dirs WHERE path=? AND child_count=0`, dir); err != nil {
				return err
			}
		}
	}
	return nil
}
