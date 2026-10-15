package services

import (
	"context"
	"database/sql"
)

const dropSemanticCountTriggers = `DROP TRIGGER IF EXISTS semantic_count_insert;
DROP TRIGGER IF EXISTS semantic_count_delete;
DROP TRIGGER IF EXISTS semantic_count_update;`

// Maintain the count in the same transaction as the rows. This also covers
// structural deletions, rollback and edits registered in multiple lists.
func installSQLiteSemanticCount(ctx context.Context, tx *sql.Tx, total int) error {
	_, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO meta VALUES('semantic_count',?);
CREATE TRIGGER IF NOT EXISTS semantic_count_insert AFTER INSERT ON records WHEN NEW.category<>'file'
BEGIN UPDATE meta SET value=CAST(value AS INTEGER)+1 WHERE key='semantic_count'; END;
CREATE TRIGGER IF NOT EXISTS semantic_count_delete AFTER DELETE ON records WHEN OLD.category<>'file'
BEGIN UPDATE meta SET value=CAST(value AS INTEGER)-1 WHERE key='semantic_count'; END;
CREATE TRIGGER IF NOT EXISTS semantic_count_update AFTER UPDATE OF category ON records
BEGIN UPDATE meta SET value=CAST(value AS INTEGER)+(NEW.category<>'file')-(OLD.category<>'file') WHERE key='semantic_count'; END;`, total)
	return err
}

// Upgrade old caches once, before the private index is installed in core. A
// payload edit must never bootstrap this count under the global core lock.
func ensureSQLiteSemanticCount(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var total int
	// Older application versions may copy meta without copying triggers.
	err = tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='semantic_count'
AND (SELECT count(*) FROM sqlite_master WHERE type='trigger' AND name IN
('semantic_count_insert','semantic_count_delete','semantic_count_update'))=3`).Scan(&total)
	if err == nil {
		return nil
	}
	if err != sql.ErrNoRows {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM records WHERE category<>?`, SearchCategoryFile).Scan(&total); err != nil {
		return err
	}
	if err := installSQLiteSemanticCount(ctx, tx, total); err != nil {
		return err
	}
	return tx.Commit()
}
