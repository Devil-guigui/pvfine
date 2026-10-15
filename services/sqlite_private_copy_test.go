package services

import (
	"path/filepath"
	"testing"
)

func TestPrivateSQLiteCopyIncludesWALAndIsolatesWrites(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source.db")
	db, err := openArchiveIndexDatabase(source, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := enableArchiveIndexWAL(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE values_test(value TEXT); INSERT INTO values_test VALUES('committed in WAL')`); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "private.db")
	if err := copyPrivateSQLiteIndex(db, source, target); err != nil {
		t.Fatal(err)
	}
	private, err := openArchiveIndexDatabase(target, false)
	if err != nil {
		t.Fatal(err)
	}
	defer private.Close()
	var value string
	if err := private.QueryRow(`SELECT value FROM values_test`).Scan(&value); err != nil || value != "committed in WAL" {
		t.Fatalf("snapshot lost WAL commit: value=%q err=%v", value, err)
	}
	if _, err := private.Exec(`UPDATE values_test SET value='private edit'`); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT value FROM values_test`).Scan(&value); err != nil || value != "committed in WAL" {
		t.Fatalf("private write changed cache: value=%q err=%v", value, err)
	}
	if _, err := db.Exec(`UPDATE values_test SET value='later source edit'`); err != nil {
		t.Fatal(err)
	}
	if err := private.QueryRow(`SELECT value FROM values_test`).Scan(&value); err != nil || value != "private edit" {
		t.Fatalf("source write changed snapshot: value=%q err=%v", value, err)
	}
}
