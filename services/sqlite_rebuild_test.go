package services

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"pvfine/internal/pvf"
)

func TestSQLiteRebuildFollowUpDoesNotWaitForClosingGate(t *testing.T) {
	c, _ := semanticDeltaFixture(t)
	c.archiveTasks.beginClose()
	defer c.archiveTasks.endClose()
	done := make(chan struct{})
	go func() {
		c.startSearchIndexForced()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("rebuild follow-up waited for archive close")
	}
}

func TestSQLiteDependencyBatchesPreserveTailAndDeduplicate(t *testing.T) {
	a := pvf.New()
	index := deltaIndexFixture(t, a)
	tx, err := index.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	writer, err := newSemanticDependencyWriter(context.Background(), tx)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.close()
	metadata := pvf.ScriptMetadata{StringTableReferences: []pvf.StringTableReference{{Index: 37, Key: "one"}, {Index: 37, Key: "two"}}}
	for n := int32(0); n < 140; n++ {
		if err := writer.write(context.Background(), a, n, metadata); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := index.db.QueryRow(`SELECT count(*) FROM semantic_dependencies WHERE dependency_path='@string-table:37'`).Scan(&count); err != nil || count != 140 {
		t.Fatalf("dependencies=%d err=%v", count, err)
	}
}

func TestSQLiteCandidateCopiesOnlyCommittedFileProjection(t *testing.T) {
	_, index := semanticDeltaFixture(t)
	// Remains in the live WAL; ATTACH must see it without requiring a checkpoint.
	if _, err := index.db.Exec(`UPDATE files SET size=123 WHERE file_index=0`); err != nil {
		t.Fatal(err)
	}
	target, err := openArchiveIndexDatabase(filepath.Join(t.TempDir(), "candidate.db"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	if err = configureArchiveIndex(target, true); err != nil {
		t.Fatal(err)
	}
	if err = copySQLiteFileProjection(context.Background(), target, index.db, index.path); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"files", "dirs"} {
		query := `SELECT * FROM ` + table + ` ORDER BY 1`
		if !reflect.DeepEqual(deltaQuery(t, target, query), deltaQuery(t, index.db, query)) {
			t.Fatalf("%s differs", table)
		}
	}
	for _, table := range []string{"records", "tags", "visuals", "semantic_dependencies"} {
		var count int
		if err = target.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s count=%d err=%v", table, count, err)
		}
	}
	var complete string
	if err = target.QueryRow(`SELECT value FROM meta WHERE key='complete'`).Scan(&complete); err != nil || complete != "0" {
		t.Fatalf("complete=%q err=%v", complete, err)
	}
	var oldRecords int
	if err = index.db.QueryRow(`SELECT count(*) FROM records`).Scan(&oldRecords); err != nil || oldRecords == 0 {
		t.Fatalf("live records=%d err=%v", oldRecords, err)
	}
}

func TestSQLiteVirtualFileSearchPaginationAndRegistration(t *testing.T) {
	c, index := semanticDeltaFixture(t)
	var fallback int
	if err := index.db.QueryRow(`SELECT count(*) FROM records WHERE category='file'`).Scan(&fallback); err != nil || fallback != 0 {
		t.Fatalf("stored fallback=%d err=%v", fallback, err)
	}
	// Match semantic names and physical paths with one wildcard, crossing sources.
	svc := NewArchiveService(c)
	var paths []string
	cursor := 0
	for page := 0; ; page++ {
		if page > 10 {
			t.Fatal("pagination did not terminate")
		}
		result, err := svc.Search("*", cursor, 1)
		if err != nil {
			t.Fatal(err)
		}
		for _, hit := range result.Hits {
			paths = append(paths, hit.Path)
		}
		if result.NextCursor < 0 {
			break
		}
		cursor = result.NextCursor
	}
	// First equipment file has two registrations; second one has one; lists
	// remain searchable as virtual file records, once each.
	if len(paths) != 5 {
		t.Fatalf("paths=%v", paths)
	}
	exact, err := index.search("equipment/equipment.lst", 0, 10, true)
	if err != nil || len(exact.Hits) != 1 || exact.Hits[0].Category != SearchCategoryFile {
		t.Fatalf("exact=%#v err=%v", exact, err)
	}
	registered, err := index.searchScopedRecords("*", 0, 10, false, false, "", "", true)
	if err != nil || len(registered.Hits) != 3 {
		t.Fatalf("registered=%#v err=%v", registered, err)
	}
	if err := NewEditorService(c).SetText(2, ""); err != nil {
		t.Fatal(err)
	}
	waitForSearchIndex(t, c)
	page, err := svc.Search("second.equ", 0, 10)
	if err != nil || len(page.Hits) != 1 || page.Hits[0].Category != SearchCategoryFile {
		t.Fatalf("removed registration=%#v err=%v", page, err)
	}
	if err := NewEditorService(c).SetText(2, "2\t`second.equ`"); err != nil {
		t.Fatal(err)
	}
	waitForSearchIndex(t, c)
	page, err = svc.Search("Second", 0, 10)
	if err != nil || len(page.Hits) != 1 || page.Hits[0].Category != SearchCategoryEquipment {
		t.Fatalf("restored registration=%#v err=%v", page, err)
	}
}

func TestSQLiteVirtualFileStreamUsesPrimaryKeyWithoutSorting(t *testing.T) {
	_, index := semanticDeltaFixture(t)
	rows, err := index.db.Query(`EXPLAIN QUERY PLAN SELECT file_index FROM files f
WHERE file_index>? AND NOT EXISTS(SELECT 1 FROM records r WHERE r.file_index=f.file_index)
ORDER BY file_index`, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(detail, "TEMP B-TREE") {
			t.Fatalf("virtual stream requires sorting: %s", detail)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}
