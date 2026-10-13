package services

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"pvfine/internal/pvf"
)

func semanticDeltaFixture(t *testing.T) (*core, *sqliteArchiveIndex) {
	t.Helper()
	a := pvf.New()
	mustAddText(t, a, "equipment/first.equ", "[name]\n`First`\n[rarity]\n2", pvf.TypeScript)
	mustAddText(t, a, "equipment/second.equ", "[name]\n`Second`", pvf.TypeScript)
	mustAddText(t, a, "equipment/equipment.lst", "1\t`first.equ`\n2\t`second.equ`\n3\t`future.equ`", pvf.TypeScript)
	mustAddText(t, a, "stackable/stackable.lst", "9\t`../equipment/first.equ`", pvf.TypeScript)
	c := NewCore()
	if err := c.setArchive(a); err != nil {
		t.Fatal(err)
	}
	index, _, err := openSQLiteArchiveIndex(a)
	if err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	c.diskIndex = index
	c.installDiskArchiveIndexesLocked(a)
	c.mu.Unlock()
	t.Cleanup(c.closeArchive)
	c.startSearchIndex()
	waitForSearchIndex(t, c)
	return c, index
}

func assertSemanticDeltaMatchesFull(t *testing.T, c *core, index *sqliteArchiveIndex) {
	t.Helper()
	waitForSearchIndex(t, c)
	c.mu.RLock()
	snapshot := c.archive.CloneForBatch()
	specs := c.searchableListSpecsLocked()
	c.mu.RUnlock()
	oracle := deltaIndexFixture(t, snapshot)
	t.Cleanup(oracle.close)
	fake := &core{archive: snapshot, diskIndex: oracle, indexGen: 1, indexStatus: IndexStatus{State: IndexStateBuilding}}
	if _, _, err := oracle.buildSemantic(context.Background(), fake, snapshot, specs, 1); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`SELECT file_index,name,record_id,path,category,list_path,size,data_type FROM records ORDER BY file_index,category,list_path,record_id`,
		`SELECT file_index,tag_id,name,category FROM tags ORDER BY file_index,category,tag_id`,
		`SELECT * FROM visuals ORDER BY file_index`,
		`SELECT * FROM semantic_dependencies ORDER BY file_index,dependency_path`,
	} {
		c.mu.RLock()
		actual := deltaQuery(t, index.db, query)
		c.mu.RUnlock()
		expected := deltaQuery(t, oracle.db, query)
		if !reflect.DeepEqual(actual, expected) {
			t.Fatalf("delta differs from full rebuild: %s\nactual=%v\nexpected=%v", query, actual, expected)
		}
	}
}

func TestSQLiteSemanticDeltaMatchesFullAndPreservesUntouchedRows(t *testing.T) {
	c, index := semanticDeltaFixture(t)
	originalDB := index.db
	untouched := deltaQuery(t, index.db, `SELECT id FROM records WHERE path='equipment/second.equ'`)
	first, _ := c.archive.Find("equipment/first.equ")
	if err := NewEditorService(c).SetText(first, "[name]\n`Updated`\n[rarity]\n4"); err != nil {
		t.Fatal(err)
	}
	assertSemanticDeltaMatchesFull(t, c, index)
	if index.db != originalDB || !reflect.DeepEqual(untouched, deltaQuery(t, index.db, `SELECT id FROM records WHERE path='equipment/second.equ'`)) {
		t.Fatal("single-file update rebuilt unrelated semantic rows")
	}
	created, err := NewArchiveService(c).CreateFile("equipment/future.equ", pvf.TypeScript)
	if err != nil {
		t.Fatal(err)
	}
	if err := NewEditorService(c).SetText(created.FileIndex, "[name]\n`Future`"); err != nil {
		t.Fatal(err)
	}
	assertSemanticDeltaMatchesFull(t, c, index)
	list, _ := c.archive.Find("equipment/equipment.lst")
	if err := NewEditorService(c).SetText(list, "2\t`second.equ`\n3\t`future.equ`"); err != nil {
		t.Fatal(err)
	}
	assertSemanticDeltaMatchesFull(t, c, index)
	first, _ = c.archive.Find("equipment/first.equ")
	if _, err := NewArchiveService(c).DeleteFiles([]int32{first}); err != nil {
		t.Fatal(err)
	}
	assertSemanticDeltaMatchesFull(t, c, index)
}

func TestSQLiteSemanticDeltaRollbackRetainsDirtyQueue(t *testing.T) {
	c, index := semanticDeltaFixture(t)
	first, _ := c.archive.Find("equipment/first.equ")
	c.mu.Lock()
	before := deltaQuery(t, index.db, `SELECT file_index,name FROM records ORDER BY id`)
	if err := c.archive.SetText(first, "[name]\n`After failure`"); err != nil {
		t.Fatal(err)
	}
	index.queueSemanticMutations(c.archive.MutationsSince(0))
	c.archive.ClearMutations()
	c.indexGen++
	gen := c.indexGen
	c.indexStatus = IndexStatus{State: IndexStateBuilding}
	specs := c.searchableListSpecsLocked()
	if _, err := index.db.Exec(`CREATE TRIGGER fail_semantic BEFORE INSERT ON records WHEN NEW.name='After failure' BEGIN SELECT RAISE(ABORT,'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	a := c.archive
	c.mu.Unlock()
	if _, _, err := index.buildSemanticDelta(context.Background(), c, a, specs, gen); err == nil {
		t.Fatal("failed delta reported success")
	}
	if !reflect.DeepEqual(before, deltaQuery(t, index.db, `SELECT file_index,name FROM records ORDER BY id`)) || len(index.pendingPaths) == 0 {
		t.Fatal("failed transaction changed records or lost pending work")
	}
	if _, err := index.db.Exec(`DROP TRIGGER fail_semantic`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := index.buildSemanticDelta(context.Background(), c, a, specs, gen); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	c.indexStatus = IndexStatus{State: IndexStateReady}
	c.mu.Unlock()
	assertSemanticDeltaMatchesFull(t, c, index)
}

func TestSQLiteDirtySearchUsesPathsAndRejectsOldPagination(t *testing.T) {
	c, index := semanticDeltaFixture(t)
	svc := NewArchiveService(c)
	first, err := svc.Search("equipment", 0, 1)
	if err != nil || first.NextCursor < 0 {
		t.Fatalf("first page=%#v err=%v", first, err)
	}
	c.mu.Lock()
	index.queueSemanticMutations(pvf.MutationSummary{Files: []pvf.FileMutation{{Path: "equipment/first.equ", Kind: pvf.MutationModified}}})
	c.indexStatus = IndexStatus{State: IndexStateBuilding}
	c.mu.Unlock()
	if name, err := index.indexedNames(0); err != ErrSearchIndexing || name != "" {
		t.Fatalf("dirty semantic name=%q err=%v", name, err)
	}
	if _, err := svc.Search("equipment", first.NextCursor, 1); err == nil {
		t.Fatal("old semantic cursor was accepted during path fallback")
	}
	partial, err := svc.Search("equipment/first", 0, 10)
	if err != nil || len(partial.Hits) != 1 || partial.Hits[0].ID != "" || partial.Hits[0].Name != "first.equ" {
		t.Fatalf("path fallback=%#v err=%v", partial, err)
	}
	if _, err := svc.SearchQuestTargets("dungeon", "1", 0, 10); err == nil {
		t.Fatal("exact semantic query presented incomplete results as ready")
	}
	c.startSearchIndex()
	waitForSearchIndex(t, c)
	if _, err := svc.Search("equipment", partial.NextCursor, 10); partial.NextCursor > 0 && err == nil {
		t.Fatal("path cursor survived publication of semantic rows")
	}
}

func TestSQLiteSemanticDeltaUpdatesLocalizedPlaceholderDependencies(t *testing.T) {
	c, a, itemIndex, _ := paged110LayoutFixture(t)
	index, _, err := openSQLiteArchiveIndex(a)
	if err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	c.diskIndex = index
	c.installDiskArchiveIndexesLocked(a)
	c.mu.Unlock()
	t.Cleanup(c.closeArchive)
	c.startSearchIndex()
	waitForSearchIndex(t, c)
	originalDB := index.db
	if err := NewEditorService(c).SetPlaceholderText(itemIndex, 3, "name_514530375", "增量本地化名称"); err != nil {
		t.Fatal(err)
	}
	assertSemanticDeltaMatchesFull(t, c, index)
	result, err := NewArchiveService(c).Search("增量本地化名称", 0, 10)
	if err != nil || len(result.Hits) != 1 {
		t.Fatalf("localized delta search=%#v err=%v", result, err)
	}
	if index.db != originalDB {
		t.Fatal("string-table edit copied the full semantic database")
	}
}

// Compare identical one-file updates on a synthetic 100,000-entry archive.
// Fixture preparation and initial indexing are outside the measured interval.
func BenchmarkSQLiteSemanticRefresh(b *testing.B) {
	for _, mode := range []string{"full", "delta"} {
		b.Run(mode, func(b *testing.B) {
			a := pvf.New()
			var list strings.Builder
			for n := 0; n < 100000; n++ {
				path := fmt.Sprintf("equipment/%06d.equ", n)
				a.AddFile(path, nil, pvf.TypeScript)
				fmt.Fprintf(&list, "%d\t`%06d.equ`\n", n, n)
			}
			if _, err := a.AddFileText("equipment/equipment.lst", list.String(), pvf.TypeScript); err != nil {
				b.Fatal(err)
			}
			index := deltaIndexFixture(b, a)
			b.Cleanup(index.close)
			c := &core{archive: a, diskIndex: index, indexGen: 1, indexStatus: IndexStatus{State: IndexStateBuilding}}
			specs := []searchableListSpec{{"equipment/equipment.lst", SearchCategoryEquipment}}
			if _, _, err := index.buildSemantic(context.Background(), c, a, specs, 1); err != nil {
				b.Fatal(err)
			}
			b.ResetTimer()
			for n := 0; n < b.N; n++ {
				b.StopTimer()
				if err := a.SetText(0, fmt.Sprintf("[name]\n`updated %d`", n)); err != nil {
					b.Fatal(err)
				}
				index.queueSemanticMutations(a.MutationsSince(0))
				a.ClearMutations()
				b.StartTimer()
				var err error
				if mode == "full" {
					_, _, err = index.buildSemantic(context.Background(), c, a, specs, 1)
				} else {
					_, _, err = index.buildSemanticDelta(context.Background(), c, a, specs, 1)
				}
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestSQLiteCacheIdentityChecksContentWithUnchangedFileMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity.pvf")
	a := pvf.New()
	mustAddText(t, a, "equipment/a.equ", "[name]\n`Alpha`", pvf.TypeScript)
	if err := a.SaveAs(path); err != nil {
		t.Fatal(err)
	}
	before, _, err := archiveIndexIdentity(a)
	if err != nil {
		t.Fatal(err)
	}
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	b := pvf.New()
	mustAddText(t, b, "equipment/a.equ", "[name]\n`Bravo`", pvf.TypeScript)
	if err := b.SaveAs(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, stat.ModTime(), stat.ModTime()); err != nil {
		t.Fatal(err)
	}
	afterStat, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if afterStat.Size() != stat.Size() || a.Header().BodySize != b.Header().BodySize {
		t.Fatal("fixture must retain the cheap cache identity metadata")
	}
	loaded, err := pvf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	after, _, err := archiveIndexIdentity(loaded)
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Fatal("content replacement reused an unrelated cache")
	}
}

func TestSQLiteRapidEditsRetainLatestSemanticSnapshot(t *testing.T) {
	c, index := semanticDeltaFixture(t)
	first, _ := c.archive.Find("equipment/first.equ")
	for n := 0; n < 40; n++ {
		if err := NewEditorService(c).SetText(first, fmt.Sprintf("[name]\n`Latest %d`", n)); err != nil {
			t.Fatal(err)
		}
	}
	assertSemanticDeltaMatchesFull(t, c, index)
	result, err := NewArchiveService(c).Search("Latest 39", 0, 10)
	if err != nil || len(result.Hits) != 2 {
		t.Fatalf("latest queued snapshot=%#v err=%v", result, err)
	}
}

func TestSQLiteForcedRebuildSurvivesConcurrentEdits(t *testing.T) {
	c, index := semanticDeltaFixture(t)
	before := index.db
	if _, err := NewArchiveService(c).RebuildSearchIndex(); err != nil {
		t.Fatal(err)
	}
	first, _ := c.archive.Find("equipment/first.equ")
	for n := 0; n < 12; n++ {
		if err := NewEditorService(c).SetText(first, fmt.Sprintf("[name]\n`Forced latest %d`", n)); err != nil {
			t.Fatal(err)
		}
	}
	assertSemanticDeltaMatchesFull(t, c, index)
	if index.db == before {
		t.Fatal("queued edits downgraded the explicit forced rebuild")
	}
}
