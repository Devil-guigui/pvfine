package services

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"pvfine/internal/pvf"
)

var errIndexDeltaStale = errors.New("索引增量的基础修订已变化")

func (i *sqliteArchiveIndex) queueSemanticMutations(summary pvf.MutationSummary) {
	if i.pendingPaths == nil {
		i.pendingPaths = make(map[string]struct{})
		i.pendingIDs = make(map[uint64]struct{})
	}
	i.pendingStructural = i.pendingStructural || summary.Structural
	for _, mutation := range summary.Files {
		if mutation.Kind != pvf.MutationModified {
			i.pendingStructural = true
		}
		i.pendingPaths[normalizeSearchPath(mutation.Path)] = struct{}{}
		if mutation.Identity != 0 {
			i.pendingIDs[mutation.Identity] = struct{}{}
		}
	}
	i.semanticEpoch++
	i.ready, i.dirty = false, true
}

type semanticDeltaRecord struct {
	index                        int32
	path, id, listPath, category string
	metadata                     pvf.ScriptMetadata
}

type semanticRelation struct{ listPath, category string }

// buildSemanticDelta reads list registrations, but parses script metadata only
// for affected files. Publication is a single WAL transaction against the live
// projection; no full candidate database is copied or indexes recreated.
func (i *sqliteArchiveIndex) buildSemanticDelta(ctx context.Context, c *core, a *pvf.Archive, specs []searchableListSpec, gen uint64) (int, int, error) {
	c.mu.RLock()
	if !c.indexIsCurrentLocked(a, gen) || ctx.Err() != nil {
		c.mu.RUnlock()
		return 0, 0, context.Canceled
	}
	epoch, revision := i.semanticEpoch, c.batchRevision
	paths, ids := make(map[string]struct{}), make(map[uint64]struct{})
	for path := range i.pendingPaths {
		paths[path] = struct{}{}
	}
	for id := range i.pendingIDs {
		ids[id] = struct{}{}
	}
	// Ordinary payload edits preserve registrations. Query only their existing
	// rows and read metadata under the archive lock; no mutable batch clone is
	// needed. Structural/list/dependency edits retain the general resolver.
	fast := !i.pendingStructural
	for path := range paths {
		if strings.HasSuffix(path, ".lst") || strings.HasSuffix(path, ".str") || isNPCEntryPath(path) {
			fast = false
		}
	}
	matchedIDs := make(map[uint64]struct{})
	for path := range paths {
		if index, found := a.Find(path); found {
			matchedIDs[a.FileIdentity(index)] = struct{}{}
		}
	}
	for id := range ids {
		if _, found := matchedIDs[id]; !found {
			fast = false // duplicate paths require the identity-aware resolver
		}
	}
	i.dbMu.RLock()
	var fingerprint string
	if err := i.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='specs'`).Scan(&fingerprint); err != nil || fingerprint != searchIndexSpecFingerprint(specs) {
		fast = false
	}
	i.dbMu.RUnlock()
	if fast {
		affected := make(map[int32]struct{})
		var replacements []semanticDeltaRecord
		for path := range paths {
			if index, found := a.Find(path); found {
				affected[index] = struct{}{}
			}
		}
		i.dbMu.RLock()
		var err error
		for index := range affected {
			rows, queryErr := i.db.QueryContext(ctx, `SELECT path,record_id,list_path,category FROM records WHERE file_index=? AND category<>?`, index, SearchCategoryFile)
			if queryErr != nil {
				err = queryErr
				break
			}
			for rows.Next() {
				record := semanticDeltaRecord{index: index}
				if err = rows.Scan(&record.path, &record.id, &record.listPath, &record.category); err != nil {
					break
				}
				replacements = append(replacements, record)
			}
			if err == nil {
				err = rows.Err()
			}
			_ = rows.Close()
			if err != nil {
				break
			}
		}
		i.dbMu.RUnlock()
		if err == nil {
			npcNames := make(map[string]string)
			for _, record := range replacements {
				if isItemShopEntry(record.listPath, record.path) {
					npcNames = buildNPCNameIndexFromArchive(a)
					break
				}
			}
			for n := range replacements {
				record := &replacements[n]
				record.metadata, _ = readIndexedMetadataFromArchive(a, record.index, record.listPath, npcNames)
			}
		}
		c.mu.RUnlock()
		if err != nil {
			return 0, 0, err
		}
		return i.publishSemanticDelta(ctx, c, a, a, specs, gen, epoch, revision, affected, replacements, 0)
	}
	snapshot := a.CloneForBatch()
	knownLists := make(map[string]struct{})
	for _, spec := range specs {
		knownLists[normalizeSearchPath(spec.listPath)] = struct{}{}
		if index, ok := snapshot.FindList(spec.listPath); ok {
			knownLists[normalizeSearchPath(snapshot.Path(index))] = struct{}{}
		}
	}
	affected := make(map[int32]struct{})
	for index := int32(0); index < snapshot.FileCount(); index++ {
		if _, changed := ids[snapshot.FileIdentity(index)]; changed {
			affected[index] = struct{}{}
		}
	}
	i.dbMu.RLock()
	oldRelations := make(map[semanticRelation]struct{})
	rows, err := i.db.QueryContext(ctx, `SELECT DISTINCT list_path,category FROM records WHERE category<>?`, SearchCategoryFile)
	if err == nil {
		for rows.Next() {
			var r semanticRelation
			if err = rows.Scan(&r.listPath, &r.category); err != nil {
				break
			}
			oldRelations[r] = struct{}{}
		}
		if err == nil {
			err = rows.Err()
		}
		_ = rows.Close()
	}
	if err == nil {
		for path := range paths {
			if strings.HasSuffix(path, ".str") || strings.HasSuffix(path, ".lst") {
				query, args := `SELECT DISTINCT file_index FROM semantic_dependencies WHERE dependency_path=?`, []any{path}
				if strings.HasSuffix(path, ".lst") {
					if _, known := knownLists[path]; known && !pvf.IsStringTableMappingPath(path) {
						continue
					}
					query, args = `SELECT DISTINCT file_index FROM semantic_dependencies`, nil
				}
				var depRows *sql.Rows
				depRows, err = i.db.QueryContext(ctx, query, args...)
				if err != nil {
					break
				}
				for depRows.Next() {
					var index int32
					if err = depRows.Scan(&index); err != nil {
						break
					}
					affected[index] = struct{}{}
				}
				if err == nil {
					err = depRows.Err()
				}
				_ = depRows.Close()
				if err != nil {
					break
				}
			}
		}
	}
	i.dbMu.RUnlock()
	c.mu.RUnlock()
	if err != nil {
		return 0, 0, err
	}
	currentRelations := make(map[semanticRelation]struct{})
	var registrations []semanticDeltaRecord
	skipped := 0
	npcChanged := false
	for path := range paths {
		if isNPCEntryPath(path) || sameSearchPath(path, npcListPath) {
			npcChanged = true
		}
	}
	for _, spec := range specs {
		if err = ctx.Err(); err != nil {
			return 0, 0, err
		}
		listIndex, found := snapshot.FindList(spec.listPath)
		if !found {
			continue
		}
		listPath := snapshot.Path(listIndex)
		r := semanticRelation{listPath, spec.category}
		currentRelations[r] = struct{}{}
		_, previouslyPresent := oldRelations[r]
		_, listChanged := paths[normalizeSearchPath(listPath)]
		_, configuredListChanged := paths[normalizeSearchPath(spec.listPath)]
		pairs, pairErr := snapshot.ListPairs(listIndex)
		if pairErr != nil {
			skipped++
			continue
		}
		for _, pair := range pairs {
			target, index, found := findListTargetInArchive(snapshot, listPath, pair.Path)
			if !found {
				skipped++
				continue
			}
			changed := !previouslyPresent || listChanged || configuredListChanged || (npcChanged && sameSearchPath(spec.listPath, itemShopListPath))
			candidates, _ := listPathCandidates(listPath, pair.Path)
			for _, candidate := range candidates {
				if _, exists := paths[normalizeSearchPath(candidate)]; exists {
					changed = true
					break
				}
			}
			if changed {
				affected[index] = struct{}{}
			}
			registrations = append(registrations, semanticDeltaRecord{index: index, path: target, id: pair.ID, listPath: listPath, category: spec.category})
		}
	}
	// Old relation targets need fallback records and tag cleanup when their list
	// is deleted, changed or removed by configuration.
	c.mu.RLock()
	if !c.indexIsCurrentLocked(a, gen) || i.semanticEpoch != epoch || c.batchRevision != revision {
		c.mu.RUnlock()
		return 0, 0, errIndexDeltaStale
	}
	i.dbMu.RLock()
	for r := range oldRelations {
		_, exists := currentRelations[r]
		_, changed := paths[normalizeSearchPath(r.listPath)]
		if exists && !changed {
			continue
		}
		rows, err = i.db.QueryContext(ctx, `SELECT file_index FROM records WHERE list_path=? AND category=?`, r.listPath, r.category)
		if err != nil {
			break
		}
		for rows.Next() {
			var index int32
			if err = rows.Scan(&index); err != nil {
				break
			}
			affected[index] = struct{}{}
		}
		if err == nil {
			err = rows.Err()
		}
		_ = rows.Close()
		if err != nil {
			break
		}
	}
	i.dbMu.RUnlock()
	c.mu.RUnlock()
	if err != nil {
		return 0, 0, err
	}
	npcNames := make(map[string]string)
	for _, record := range registrations {
		if _, needed := affected[record.index]; needed && isItemShopEntry(record.listPath, record.path) {
			npcNames = buildNPCNameIndexFromArchive(snapshot)
			break
		}
	}
	metadataCache := make(map[int32]pvf.ScriptMetadata)
	var replacements []semanticDeltaRecord
	for _, record := range registrations {
		if _, needed := affected[record.index]; !needed {
			continue
		}
		if err = ctx.Err(); err != nil {
			return 0, 0, err
		}
		metadata, cached := metadataCache[record.index]
		if !cached || isItemShopEntry(record.listPath, record.path) {
			metadata, err = readIndexedMetadataFromArchive(snapshot, record.index, record.listPath, npcNames)
			if err != nil {
				metadata = pvf.ScriptMetadata{}
			}
			if !isItemShopEntry(record.listPath, record.path) {
				metadataCache[record.index] = metadata
			}
		}
		record.metadata = metadata
		replacements = append(replacements, record)
	}
	return i.publishSemanticDelta(ctx, c, a, snapshot, specs, gen, epoch, revision, affected, replacements, skipped)
}

// The revision guard allows the fast path to reuse the live archive safely.
// A concurrent mutation is retried before reading any file or dependencies.
func (i *sqliteArchiveIndex) publishSemanticDelta(ctx context.Context, c *core, a, snapshot *pvf.Archive, specs []searchableListSpec, gen, epoch, revision uint64, affected map[int32]struct{}, replacements []semanticDeltaRecord, skipped int) (int, int, error) {
	var err error
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.indexIsCurrentLocked(a, gen) || ctx.Err() != nil {
		return 0, 0, context.Canceled
	}
	if i.semanticEpoch != epoch || c.batchRevision != revision {
		return 0, 0, errIndexDeltaStale
	}
	i.dbMu.Lock()
	defer i.dbMu.Unlock()
	if err = i.detachCacheLocked(); err != nil {
		return 0, 0, err
	}
	tx, err := i.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `CREATE TEMP TABLE semantic_affected(file_index INTEGER PRIMARY KEY)`); err != nil {
		return 0, 0, err
	}
	insertAffected, err := tx.PrepareContext(ctx, `INSERT INTO semantic_affected VALUES(?)`)
	if err != nil {
		return 0, 0, err
	}
	defer insertAffected.Close()
	for index := range affected {
		file := snapshot.File(index)
		if _, err = tx.ExecContext(ctx, `UPDATE files SET size=?,data_type=?,change_kind=? WHERE file_index=?`, file.DataSize, file.DataType, archiveChangeKind(snapshot, index), index); err != nil {
			return 0, 0, err
		}
		if _, err = insertAffected.ExecContext(ctx, index); err != nil {
			return 0, 0, err
		}
	}
	for _, table := range []string{"records", "tags", "visuals", "semantic_dependencies"} {
		if _, err = tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE file_index IN (SELECT file_index FROM semantic_affected)`); err != nil {
			return 0, 0, err
		}
	}
	visualsWritten := make(map[int32]struct{})
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO records(file_index,name,lower_name,record_id,lower_id,path,lower_path,category,list_path,size,data_type) VALUES(?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return 0, 0, err
	}
	defer stmt.Close()
	dependencies, err := newSemanticDependencyWriter(ctx, tx)
	if err != nil {
		return 0, 0, err
	}
	defer dependencies.close()
	for _, record := range replacements {
		file, metadata := snapshot.File(record.index), record.metadata
		name := markedName(metadata)
		if _, err = stmt.ExecContext(ctx, record.index, name, strings.ToLower(name), record.id, strings.ToLower(record.id), record.path, strings.ToLower(record.path), record.category, record.listPath, file.DataSize, file.DataType); err != nil {
			return 0, 0, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT OR REPLACE INTO tags VALUES(?,?,?,?)`, record.index, record.id, name, record.category); err != nil {
			return 0, 0, err
		}
		if _, written := visualsWritten[record.index]; !written {
			icon, field := visualsFromMetadata(metadata).icon, visualsFromMetadata(metadata).fieldImage
			iconPath, fieldPath := "", ""
			iconIndex, fieldIndex := int32(-1), int32(-1)
			if icon != nil {
				iconPath, iconIndex = icon.Path, icon.Index
			}
			if field != nil {
				fieldPath, fieldIndex = field.Path, field.Index
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO visuals VALUES(?,?,?,?,?,?)`, record.index, iconPath, iconIndex, fieldPath, fieldIndex, metadata.RarityValue()); err != nil {
				return 0, 0, err
			}
			if err = dependencies.write(ctx, snapshot, record.index, metadata); err != nil {
				return 0, 0, err
			}
			visualsWritten[record.index] = struct{}{}
		}
	}
	if err = dependencies.flush(ctx); err != nil {
		return 0, 0, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE meta SET value='1' WHERE key='complete'; INSERT OR REPLACE INTO meta VALUES('specs',?); DROP TABLE semantic_affected`, searchIndexSpecFingerprint(specs)); err != nil {
		return 0, 0, err
	}
	var total int
	if err = tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='semantic_count'`).Scan(&total); err != nil {
		return 0, 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, 0, err
	}
	i.pendingPaths, i.pendingIDs = nil, nil
	i.pendingStructural = false
	i.ready, i.dirty = true, false
	i.semanticPublishedEpoch, i.semanticPublishedRevision = epoch, revision
	developmentLog("[pvfine:index] sqlite delta files=%d metadata=%d specs=%d", len(affected), len(replacements), len(specs))
	return total, skipped, nil
}

// semanticDependencyWriter reuses SQL and mapping expansions for one build.
// A file can reference the same table through many tokens; emit each dependency once.
type semanticDependencyWriter struct {
	tx    *sql.Tx
	stmts map[int]*sql.Stmt
	args  []any
	paths map[int][]string
}

func newSemanticDependencyWriter(ctx context.Context, tx *sql.Tx) (*semanticDependencyWriter, error) {
	w := &semanticDependencyWriter{tx: tx, stmts: make(map[int]*sql.Stmt), args: make([]any, 0, 256), paths: make(map[int][]string)}
	if _, err := w.statement(ctx, 128); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *semanticDependencyWriter) statement(ctx context.Context, rows int) (*sql.Stmt, error) {
	if stmt := w.stmts[rows]; stmt != nil {
		return stmt, nil
	}
	query := "INSERT OR IGNORE INTO semantic_dependencies VALUES" + strings.TrimSuffix(strings.Repeat("(?,?),", rows), ",")
	stmt, err := w.tx.PrepareContext(ctx, query)
	if err == nil {
		w.stmts[rows] = stmt
	}
	return stmt, err
}

func (w *semanticDependencyWriter) flush(ctx context.Context) error {
	if len(w.args) == 0 {
		return nil
	}
	stmt, err := w.statement(ctx, len(w.args)/2)
	if err != nil {
		return err
	}
	if _, err = stmt.ExecContext(ctx, w.args...); err != nil {
		return err
	}
	clear(w.args)
	w.args = w.args[:0]
	return nil
}

func (w *semanticDependencyWriter) close() {
	for _, stmt := range w.stmts {
		_ = stmt.Close()
	}
}

func (w *semanticDependencyWriter) write(ctx context.Context, a *pvf.Archive, index int32, metadata pvf.ScriptMetadata) error {
	seen := make(map[string]struct{})
	for _, reference := range metadata.StringTableReferences {
		paths, ok := w.paths[reference.Index]
		if !ok {
			paths = []string{fmt.Sprintf("@string-table:%d", reference.Index)}
			for _, path := range a.StringTableDependencyPaths(reference.Index) {
				paths = append(paths, normalizeSearchPath(path))
			}
			w.paths[reference.Index] = paths
		}
		for _, path := range paths {
			if _, exists := seen[path]; exists {
				continue
			}
			seen[path] = struct{}{}
			w.args = append(w.args, index, path)
			if len(w.args) == cap(w.args) {
				if err := w.flush(ctx); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
