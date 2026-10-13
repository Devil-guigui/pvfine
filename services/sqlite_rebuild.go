package services

import (
	"context"
	"database/sql"
	"errors"
)

// copySQLiteFileProjection creates empty semantic tables and copies only the
// canonical file projection. The live database remains readable until publish.
// ATTACH reads WAL commits in a single snapshot, including unsaved file edits.
func copySQLiteFileProjection(ctx context.Context, target, source *sql.DB, sourcePath string) error {
	if source == nil {
		return errors.New("SQLite 索引连接已关闭")
	}
	rows, err := source.QueryContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name IN ('meta','files','dirs','records','tags','visuals','semantic_dependencies') ORDER BY name`)
	if err != nil {
		return err
	}
	var schema []string
	for rows.Next() {
		var statement string
		if err = rows.Scan(&statement); err != nil {
			break
		}
		schema = append(schema, statement)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	if len(schema) != 7 {
		return errors.New("SQLite 文件投影 schema 不完整")
	}
	conn, err := target.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, `ATTACH DATABASE ? AS source_projection`, sourcePath); err != nil {
		return err
	}
	defer conn.ExecContext(context.Background(), `DETACH DATABASE source_projection`)
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, statement := range schema {
		if _, err = tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	for _, table := range []string{"meta", "files", "dirs"} {
		if _, err = tx.ExecContext(ctx, `INSERT INTO main.`+table+` SELECT * FROM source_projection.`+table); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE meta SET value='0' WHERE key='complete';
CREATE INDEX files_parent_name ON files(parent,name,file_index);
CREATE INDEX files_path ON files(path,file_index);
CREATE INDEX dirs_parent_name ON dirs(parent,name);`); err != nil {
		return err
	}
	return tx.Commit()
}
