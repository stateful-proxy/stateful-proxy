package cache

import (
	"log"

	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitemigration"
	"zombiezen.com/go/sqlite/sqlitex"
)

type DB struct {
	Pool *sqlitemigration.Pool
}

func NewDB(database_uri string) *DB {
	schema := sqlitemigration.Schema{
		Migrations: []string{
			`
CREATE TABLE reqs (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	scheme TEXT NOT NULL,
	host_and_port TEXT NOT NULL,
	path TEXT NOT NULL,
	method TEXT NOT NULL,
	headers TEXT NOT NULL,
	created_at_ms INTEGER NOT NULL,
	body BLOB NULL
);

CREATE UNIQUE INDEX idx_reqs_unique ON reqs (host_and_port, path, method, headers);

CREATE TABLE resps (
	req_id INTEGER PRIMARY KEY,
	status INTEGER NOT NULL,
	headers TEXT NOT NULL,
	created_at_ms INTEGER NOT NULL,
	body BLOB NULL,
	FOREIGN KEY (req_id) REFERENCES reqs(id)
)`,
		},
	}

	pool := sqlitemigration.NewPool(database_uri, schema, sqlitemigration.Options{
		Flags: sqlite.OpenReadWrite | sqlite.OpenCreate,
		PrepareConn: func(conn *sqlite.Conn) error {
			return sqlitex.ExecuteTransient(conn, "PRAGMA foreign_keys = ON;", nil)
		},
		OnError: func(e error) {
			log.Fatal(e)
		},
	})
	return &DB{Pool: pool}
}

func (db *DB) Close() error {
	return db.Pool.Close()
}
