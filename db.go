package main

import (
	"context"
	"log"

	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitemigration"
	"zombiezen.com/go/sqlite/sqlitex"
)

type DB struct {
	pool *sqlitemigration.Pool
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
	return &DB{pool: pool}
}

func (db *DB) Close() error {
	return db.pool.Close()
}

func (db *DB) getReqId(
	conn *sqlite.Conn, scheme, host_and_port, path, method, headers string,
) (reqID int64, err error) {
	err = sqlitex.Execute(
		conn,
		`SELECT id FROM reqs WHERE scheme = ? AND host_and_port = ? AND path = ? AND method = ? AND headers = ?`,
		&sqlitex.ExecOptions{
			ResultFunc: func(stmt *sqlite.Stmt) error {
				reqID = stmt.ColumnInt64(0)
				return nil
			},
			Args: []any{scheme, host_and_port, path, method, headers},
		},
	)
	return reqID, err
}

func (db *DB) GetReqId(
	ctx context.Context, scheme, host_and_port, path, method, headers string,
) (reqID int64, err error) {
	conn, err := db.pool.Get(ctx)
	if err != nil {
		return 0, err
	}
	defer db.pool.Put(conn)

	return db.getReqId(conn, scheme, host_and_port, path, method, headers)
}
