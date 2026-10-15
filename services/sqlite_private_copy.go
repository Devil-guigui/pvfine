package services

import (
	"context"
	"database/sql"
	"os"
)

// Clone only a checkpointed main database while excluding concurrent writers.
// The SQLite backup API remains the portable fallback, including busy WALs.
func copyPrivateSQLiteIndex(db *sql.DB, source, target string) error {
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	var busy, pages, copied int
	checkpointErr := conn.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &pages, &copied)
	cloned := false
	if checkpointErr == nil && busy == 0 {
		if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err == nil {
			// A writer could have committed between checkpoint and BEGIN.
			info, statErr := os.Stat(source + "-wal")
			if os.IsNotExist(statErr) || (statErr == nil && info.Size() == 0) {
				cloned = cloneIndexFile(source, target) == nil
			}
			_, _ = conn.ExecContext(ctx, "ROLLBACK")
		}
	}
	_ = conn.Close()
	if cloned {
		developmentLog("[pvfine:index] private SQLite snapshot mode=clone")
		return nil
	}
	_ = os.Remove(target)
	return backupSQLiteDatabase(ctx, db, target)
}
