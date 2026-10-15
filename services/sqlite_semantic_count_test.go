package services

import (
	"context"
	"testing"
)

func TestSQLiteSemanticCountUpgradeAndTransactions(t *testing.T) {
	_, index := semanticDeltaFixture(t)
	ctx := context.Background()
	// A legacy cache has rows but no counter or maintenance triggers.
	if _, err := index.db.Exec(dropSemanticCountTriggers + `DELETE FROM meta WHERE key='semantic_count';`); err != nil {
		t.Fatal(err)
	}
	for pass := 0; pass < 2; pass++ {
		if err := ensureSQLiteSemanticCount(ctx, index.db); err != nil {
			t.Fatal(err)
		}
	}
	assertCount := func() {
		t.Helper()
		var cached, actual int
		if err := index.db.QueryRow(`SELECT value FROM meta WHERE key='semantic_count'`).Scan(&cached); err != nil {
			t.Fatal(err)
		}
		if err := index.db.QueryRow(`SELECT count(*) FROM records WHERE category<>'file'`).Scan(&actual); err != nil {
			t.Fatal(err)
		}
		if cached != actual {
			t.Fatalf("cached count=%d actual=%d", cached, actual)
		}
	}
	assertCount()
	// An older candidate writer can preserve meta but omit its triggers.
	if _, err := index.db.Exec(`DROP TRIGGER semantic_count_insert; UPDATE meta SET value='999' WHERE key='semantic_count'`); err != nil {
		t.Fatal(err)
	}
	if err := ensureSQLiteSemanticCount(ctx, index.db); err != nil {
		t.Fatal(err)
	}
	assertCount()
	for _, query := range []string{
		`INSERT INTO records(file_index,name,lower_name,record_id,lower_id,path,lower_path,category,list_path,size,data_type)
SELECT file_index,name,lower_name,'new','new',path,lower_path,category,list_path,size,data_type FROM records LIMIT 1`,
		`UPDATE records SET category='file' WHERE record_id='new'`,
		`UPDATE records SET category='equipment' WHERE record_id='new'`,
		`DELETE FROM records WHERE file_index=0`,
	} {
		if _, err := index.db.Exec(query); err != nil {
			t.Fatal(err)
		}
		assertCount()
	}
	tx, err := index.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM records`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	assertCount()
}
