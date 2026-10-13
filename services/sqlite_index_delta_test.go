package services

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"pvfine/internal/pvf"
)

func deltaIndexFixture(t testing.TB, a *pvf.Archive) *sqliteArchiveIndex {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "index.db")
	db, err := openArchiveIndexDatabase(path, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err = configureArchiveIndex(db, false); err != nil {
		t.Fatal(err)
	}
	if err = enableArchiveIndexWAL(db); err != nil {
		t.Fatal(err)
	}
	if err = buildSQLiteFileIndex(db, a, "delta-test"); err != nil {
		t.Fatal(err)
	}
	a.ClearMutations()
	return &sqliteArchiveIndex{db: db, path: path, tempDir: dir, entries: archiveEntryIndexes(a)}
}

func assertDeltaProjection(t *testing.T, index *sqliteArchiveIndex, a *pvf.Archive) {
	t.Helper()
	oracle := deltaIndexFixture(t, a.CloneForBatch())
	for _, query := range []string{
		`SELECT file_index,path,lower_path,name,lower_name,parent,size,data_type,change_kind FROM files ORDER BY file_index`,
		`SELECT path,parent,name,child_count FROM dirs ORDER BY path`,
	} {
		actual, expected := deltaQuery(t, index.db, query), deltaQuery(t, oracle.db, query)
		if !reflect.DeepEqual(actual, expected) {
			t.Fatalf("projection differs from full rebuild for %s\nactual=%v\nexpected=%v", query, actual, expected)
		}
	}
}

func deltaQuery(t *testing.T, db *sql.DB, query string) [][]any {
	t.Helper()
	rows, err := db.Query(query)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var values [][]any
	for rows.Next() {
		row, pointers := make([]any, len(columns)), make([]any, len(columns))
		for index := range row {
			pointers[index] = &row[index]
		}
		if err = rows.Scan(pointers...); err != nil {
			t.Fatal(err)
		}
		values = append(values, row)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	return values
}

func TestSQLiteArchiveDeltaMatchesFullProjection(t *testing.T) {
	a := pvf.New()
	for _, path := range []string{"alone/a.str", "dir/b.str", "dir/sub/c.str", "root.str", "last/d.str"} {
		a.AddFile(path, []byte{1, 2}, pvf.TypeUnicode)
	}
	index := deltaIndexFixture(t, a)
	originalDB := index.db
	stage := a.CloneForBatch()
	if _, err := stage.RemoveFiles([]int32{0, 2, 4}); err != nil {
		t.Fatal(err)
	}
	stage.AddFile("new/deep/file.str", []byte{3, 4, 5, 6}, pvf.TypeUnicode)
	if err := index.applyArchiveDelta(stage); err != nil {
		t.Fatal(err)
	}
	assertDeltaProjection(t, index, stage)
	stage.ClearMutations()
	if _, err := stage.RemoveFiles([]int32{0}); err != nil {
		t.Fatal(err)
	}
	stage.AddFile("dir/b.str", []byte{7, 8}, pvf.TypeUnicode)
	if err := stage.SetDataType(0, pvf.TypeScript); err != nil {
		t.Fatal(err)
	}
	if err := index.applyArchiveDelta(stage); err != nil {
		t.Fatal(err)
	}
	assertDeltaProjection(t, index, stage)
	if index.db != originalDB {
		t.Fatal("delta replaced the workspace database")
	}
}

func TestSQLiteArchiveDeltaRebindsSemanticRows(t *testing.T) {
	a := pvf.New()
	for i := 0; i < 6; i++ {
		a.AddFile(fmt.Sprintf("dir/%d.str", i), nil, pvf.TypeUnicode)
	}
	index := deltaIndexFixture(t, a)
	for i := 0; i < 6; i++ {
		for _, query := range []string{
			`INSERT INTO records(file_index,name,lower_name,record_id,lower_id,path,lower_path,category,list_path,size,data_type) VALUES(?, 'keep','keep','','','path','path','equipment','',0,0)`,
			`INSERT INTO tags(file_index,tag_id,name,category) VALUES(?, 'keep', 'keep', 'equipment')`,
			`INSERT INTO visuals(file_index,icon_path,icon_index,field_path,field_index) VALUES(?, 'keep', 1, '', 0)`,
		} {
			if _, err := index.db.Exec(query, i); err != nil {
				t.Fatal(err)
			}
		}
	}
	stage := a.CloneForBatch()
	if _, err := stage.RemoveFiles([]int32{0, 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := stage.RemoveFiles([]int32{2}); err != nil { // removes original index 4
		t.Fatal(err)
	}
	if err := index.applyArchiveDelta(stage); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"records", "tags", "visuals"} {
		actual := deltaQuery(t, index.db, `SELECT file_index FROM `+table+` ORDER BY file_index`)
		expected := [][]any{{int64(0)}, {int64(1)}, {int64(2)}}
		if !reflect.DeepEqual(actual, expected) {
			t.Fatalf("%s indexes=%v, want %v", table, actual, expected)
		}
	}
	if actual := deltaQuery(t, index.db, `SELECT file_index,id FROM records ORDER BY file_index`); !reflect.DeepEqual(actual, [][]any{{int64(0), int64(2)}, {int64(1), int64(4)}, {int64(2), int64(6)}}) {
		t.Fatalf("semantic identities no longer match surviving files: %v", actual)
	}
	assertDeltaProjection(t, index, stage)
}

func TestSQLiteArchiveDeltaRollbackAndRetry(t *testing.T) {
	a := pvf.New()
	a.AddFile("old/file.str", nil, pvf.TypeUnicode)
	index := deltaIndexFixture(t, a)
	if _, err := index.db.Exec(`CREATE TRIGGER fail_delta BEFORE INSERT ON files WHEN NEW.path='new/file.str' BEGIN SELECT RAISE(ABORT,'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	stage := a.CloneForBatch()
	_, _ = stage.RemoveFiles([]int32{0})
	stage.AddFile("new/file.str", nil, pvf.TypeUnicode)
	beforeIDs := append([]uint64(nil), index.entries...)
	if err := index.applyArchiveDelta(stage); err == nil {
		t.Fatal("failed SQL transaction succeeded")
	}
	assertDeltaProjection(t, index, a)
	if !reflect.DeepEqual(beforeIDs, index.entries) {
		t.Fatal("failed transaction installed new identities")
	}
	if _, err := index.db.Exec(`DROP TRIGGER fail_delta`); err != nil {
		t.Fatal(err)
	}
	if err := index.applyArchiveDelta(stage); err != nil {
		t.Fatal(err)
	}
	assertDeltaProjection(t, index, stage)
}

func TestSQLiteArchiveDeltaKeepsCleanCacheImmutable(t *testing.T) {
	a := pvf.New()
	a.AddFile("old/file.str", nil, pvf.TypeUnicode)
	index := deltaIndexFixture(t, a)
	cachePath := index.path
	index.tempDir = "" // fixture now represents a persistent clean cache
	t.Cleanup(index.close)
	stage := a.CloneForBatch()
	stage.AddFile("new/file.str", nil, pvf.TypeUnicode)
	if err := index.applyArchiveDelta(stage); err != nil {
		t.Fatal(err)
	}
	if index.path == cachePath || index.tempDir == "" {
		t.Fatal("first mutation did not detach the clean cache")
	}
	cache, err := openArchiveIndexDatabase(cachePath, false)
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	var count int
	if err := cache.QueryRow(`SELECT count(*) FROM files`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("clean cache changed: count=%d err=%v", count, err)
	}
	workspace := index.db
	stage.ClearMutations()
	stage.AddFile("another.str", nil, pvf.TypeUnicode)
	if err := index.applyArchiveDelta(stage); err != nil {
		t.Fatal(err)
	}
	if index.db != workspace {
		t.Fatal("second mutation copied the database again")
	}
	assertDeltaProjection(t, index, stage)
}

func deltaCoreFixture(t *testing.T) (*core, *sqliteArchiveIndex) {
	t.Helper()
	a := pvf.New()
	a.AddFile("old/file.str", nil, pvf.TypeUnicode)
	c := NewCore()
	if err := c.setArchive(a); err != nil {
		t.Fatal(err)
	}
	index := deltaIndexFixture(t, a)
	c.mu.Lock()
	c.diskIndex = index
	c.installDiskArchiveIndexesLocked(a)
	c.mu.Unlock()
	t.Cleanup(c.closeArchive)
	return c, index
}

func TestSQLiteCreateAndDeleteFailuresPreserveLiveArchive(t *testing.T) {
	for _, operation := range []string{"create", "delete", "import"} {
		t.Run(operation, func(t *testing.T) {
			c, index := deltaCoreFixture(t)
			original := c.archive
			trigger := `CREATE TRIGGER fail_delta BEFORE INSERT ON files BEGIN SELECT RAISE(ABORT,'injected failure'); END`
			if operation == "delete" {
				trigger = `CREATE TRIGGER fail_delta BEFORE DELETE ON files BEGIN SELECT RAISE(ABORT,'injected failure'); END`
			}
			if _, err := index.db.Exec(trigger); err != nil {
				t.Fatal(err)
			}
			svc := NewArchiveService(c)
			var err error
			switch operation {
			case "create":
				_, err = svc.CreateFile("new/file.str", pvf.TypeUnicode)
			case "delete":
				_, err = svc.DeleteFiles([]int32{0})
			case "import":
				source := filepath.Join(t.TempDir(), "new.str")
				if err := os.WriteFile(source, []byte("text"), 0600); err != nil {
					t.Fatal(err)
				}
				_, err = svc.ImportFiles([]string{source}, "new", "text")
			}
			if err == nil {
				t.Fatal("injected SQL failure did not reject the operation")
			}
			if c.archive != original || c.archive.FileCount() != 1 || c.archive.Path(0) != "old/file.str" {
				t.Fatal("failed operation published staged archive")
			}
			assertDeltaProjection(t, index, original)
		})
	}
}

func TestSQLiteLiveMutationFailureInvalidatesProjection(t *testing.T) {
	c, index := deltaCoreFixture(t)
	if _, err := index.db.Exec(`CREATE TRIGGER fail_delta BEFORE DELETE ON files BEGIN SELECT RAISE(ABORT,'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	_, _ = c.archive.RemoveFiles([]int32{0})
	err := c.rebuildArchiveIndexesLocked(c.archive)
	c.mu.Unlock()
	if err == nil {
		t.Fatal("failed live mutation reported success")
	}
	if _, err := NewArchiveService(c).ListChildren(""); err == nil {
		t.Fatal("inconsistent projection remained readable")
	}
	if index.entries != nil || c.indexStatus.State != IndexStateError {
		t.Fatal("failed live projection was not marked for repair")
	}
	if _, err := NewArchiveService(c).RebuildSearchIndex(); err != nil {
		t.Fatalf("manual repair failed: %v", err)
	}
	if children, err := NewArchiveService(c).ListChildren(""); err != nil || len(children) != 0 {
		t.Fatalf("repaired projection children=%v err=%v", children, err)
	}
}

func TestSQLiteServiceStructuralEditsReuseProjection(t *testing.T) {
	c, index := deltaCoreFixture(t)
	svc := NewArchiveService(c)
	created, err := svc.CreateFile("new/deep/file.str", pvf.TypeUnicode)
	if err != nil {
		t.Fatal(err)
	}
	if created.FileIndex != 1 {
		t.Fatalf("created index=%d", created.FileIndex)
	}
	if _, err := svc.DeleteFiles([]int32{0}); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "file.str")
	if err := os.WriteFile(source, []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := svc.ImportFiles([]string{source}, "new/deep", ImportModeText)
	if err != nil {
		t.Fatal(err)
	}
	if result.OverwrittenCount != 1 {
		t.Fatalf("overwrite count=%d", result.OverwrittenCount)
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.diskIndex != index {
		t.Fatal("structural service operation rebuilt the file database")
	}
	assertDeltaProjection(t, index, c.archive)
	text, err := c.archive.Text(0)
	if err != nil || text != "replacement" {
		t.Fatalf("imported content=%q err=%v", text, err)
	}
}

func TestSQLiteIdenticalImportPreservesReadyIndex(t *testing.T) {
	c, index := deltaCoreFixture(t)
	index.ready = true
	c.indexStatus = IndexStatus{State: IndexStateReady}
	original := c.archive
	source := filepath.Join(t.TempDir(), "file.str")
	if err := os.WriteFile(source, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewArchiveService(c).ImportFiles([]string{source}, "old", ImportModeText); err != nil {
		t.Fatal(err)
	}
	if c.archive != original || !index.ready || index.dirty || c.indexStatus.State != IndexStateReady {
		t.Fatal("identical import invalidated the ready index")
	}
}
