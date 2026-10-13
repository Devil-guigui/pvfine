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
	for _, mutation := range summary.Files {
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
			if err = writeSemanticDependencies(ctx, tx, snapshot, record.index, metadata); err != nil {
				return 0, 0, err
			}
			visualsWritten[record.index] = struct{}{}
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO records(file_index,name,lower_name,record_id,lower_id,path,lower_path,category,list_path,size,data_type)
SELECT f.file_index,f.name,f.lower_name,'','',f.path,f.lower_path,'file','',f.size,f.data_type FROM files f JOIN semantic_affected a ON a.file_index=f.file_index WHERE NOT EXISTS(SELECT 1 FROM records r WHERE r.file_index=f.file_index)`); err != nil {
		return 0, 0, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE meta SET value='1' WHERE key='complete'; INSERT OR REPLACE INTO meta VALUES('specs',?); DROP TABLE semantic_affected`, searchIndexSpecFingerprint(specs)); err != nil {
		return 0, 0, err
	}
	var total int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM records WHERE category<>?`, SearchCategoryFile).Scan(&total); err != nil {
		return 0, 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, 0, err
	}
	i.pendingPaths, i.pendingIDs = nil, nil
	i.ready, i.dirty = true, false
	i.semanticPublishedEpoch, i.semanticPublishedRevision = epoch, revision
	developmentLog("[pvfine:index] sqlite delta files=%d metadata=%d relations=%d", len(affected), len(replacements), len(currentRelations))
	return total, skipped, nil
}

func writeSemanticDependencies(ctx context.Context, tx *sql.Tx, a *pvf.Archive, index int32, metadata pvf.ScriptMetadata) error {
	for _, reference := range metadata.StringTableReferences {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO semantic_dependencies VALUES(?,?)`, index, fmt.Sprintf("@string-table:%d", reference.Index)); err != nil {
			return err
		}
		for _, path := range a.StringTableDependencyPaths(reference.Index) {
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO semantic_dependencies VALUES(?,?)`, index, normalizeSearchPath(path)); err != nil {
				return err
			}
		}
	}
	return nil
}
