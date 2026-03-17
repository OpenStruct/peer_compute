package store

import (
	"database/sql"
	_ "embed"
)

//go:embed schema.sql
var schema string

// Migrate applies the database schema. Uses IF NOT EXISTS so it is safe to call on every startup.
func Migrate(db *sql.DB) error {
	_, err := db.Exec(schema)
	return err
}
