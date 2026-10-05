// Package sqlstore provides transactional document persistence for SQL backends.
// BSON preserves all private persistence fields, independently of public JSON DTOs.
package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	_ "github.com/jackc/pgx/v5/stdlib"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	_ "modernc.org/sqlite"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var Missing = errors.New("record not found")
var Conflict = errors.New("record conflict")

type DB struct {
	SQL *sql.DB
	mu  sync.Mutex
}
type Tx struct {
	tx  *sql.Tx
	ctx context.Context
}

func Open(ctx context.Context, driver, dsn string) (*DB, error) {
	if driver == "sqlite" {
		if !filepath.IsAbs(dsn) {
			return nil, errors.New("sqlite path must be absolute")
		}
		if e := os.MkdirAll(filepath.Dir(dsn), 0700); e != nil {
			return nil, e
		}
		f, e := os.OpenFile(dsn, os.O_CREATE|os.O_RDWR, 0600)
		if e != nil {
			return nil, e
		}
		f.Close()
	} else if driver == "postgres" {
		driver = "pgx"
	} else {
		return nil, errors.New("unsupported SQL driver")
	}
	db, e := sql.Open(driver, dsn)
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	fail := func(e error) (*DB, error) { db.Close(); return nil, e }
	if driver == "sqlite" {
		for _, q := range []string{"PRAGMA journal_mode=WAL", "PRAGMA synchronous=FULL", "PRAGMA busy_timeout=5000"} {
			if _, e = db.ExecContext(ctx, q); e != nil {
				return fail(e)
			}
		}
	}
	if _, e = db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS sourcegraph_documents (namespace TEXT NOT NULL, id TEXT NOT NULL, payload BYTEA NOT NULL, expires_at BIGINT NOT NULL DEFAULT 0, PRIMARY KEY(namespace,id))`); e != nil {
		return fail(e)
	}
	if _, e = db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS sourcegraph_schema (version INTEGER PRIMARY KEY)`); e != nil {
		return fail(e)
	}
	if _, e = db.ExecContext(ctx, `INSERT INTO sourcegraph_schema(version) VALUES(1) ON CONFLICT(version) DO NOTHING`); e != nil {
		return fail(e)
	}
	return &DB{SQL: db}, nil
}
func (d *DB) Close() error { return d.SQL.Close() }
func (d *DB) Write(ctx context.Context, fn func(*Tx) error) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, e := d.SQL.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if e = fn(&Tx{tx, ctx}); e != nil {
		return e
	}
	return tx.Commit()
}
func (t *Tx) Get(ns, id string, v any) error {
	var b []byte
	e := t.tx.QueryRowContext(t.ctx, `SELECT payload FROM sourcegraph_documents WHERE namespace=$1 AND id=$2`, ns, id).Scan(&b)
	if errors.Is(e, sql.ErrNoRows) {
		return Missing
	}
	if e != nil {
		return e
	}
	return bson.Unmarshal(b, v)
}
func (t *Tx) Put(ns, id string, v any) error {
	b, e := bson.Marshal(v)
	if e != nil {
		return e
	}
	var document bson.M
	if e = bson.Unmarshal(b, &document); e != nil {
		return e
	}
	var expires int64
	if v, ok := document["expires_at"].(primitive.DateTime); ok {
		expires = v.Time().Unix()
	}
	if ns == "observations" {
		expires = time.Now().Add(30 * 24 * time.Hour).Unix()
	}
	_, e = t.tx.ExecContext(t.ctx, `INSERT INTO sourcegraph_documents(namespace,id,payload,expires_at) VALUES($1,$2,$3,$4) ON CONFLICT(namespace,id) DO UPDATE SET payload=excluded.payload,expires_at=excluded.expires_at`, ns, id, b, expires)
	return e
}
func (t *Tx) Delete(ns, id string) error {
	_, e := t.tx.ExecContext(t.ctx, `DELETE FROM sourcegraph_documents WHERE namespace=$1 AND id=$2`, ns, id)
	return e
}
func (t *Tx) Clear(ns string) error {
	_, e := t.tx.ExecContext(t.ctx, `DELETE FROM sourcegraph_documents WHERE namespace=$1`, ns)
	return e
}
func (d *DB) Get(ctx context.Context, ns, id string, v any) error {
	var b []byte
	e := d.SQL.QueryRowContext(ctx, `SELECT payload FROM sourcegraph_documents WHERE namespace=$1 AND id=$2`, ns, id).Scan(&b)
	if errors.Is(e, sql.ErrNoRows) {
		return Missing
	}
	if e != nil {
		return e
	}
	return bson.Unmarshal(b, v)
}
func (d *DB) List(ctx context.Context, ns, after string, limit int) ([][]byte, error) {
	if limit < 1 {
		return nil, fmt.Errorf("invalid limit")
	}
	rows, e := d.SQL.QueryContext(ctx, `SELECT payload FROM sourcegraph_documents WHERE namespace=$1 AND id>$2 ORDER BY id LIMIT $3`, ns, after, limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := [][]byte{}
	for rows.Next() {
		var b []byte
		if e = rows.Scan(&b); e != nil {
			return nil, e
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
func Decode(b []byte, v any) error { return bson.Unmarshal(b, v) }
func IsPostgresDSN(s string) bool {
	return strings.HasPrefix(s, "postgres://") || strings.HasPrefix(s, "postgresql://")
}

func (d *DB) Prune(ctx context.Context) error {
	_, e := d.SQL.ExecContext(ctx, `DELETE FROM sourcegraph_documents WHERE expires_at>0 AND expires_at<$1`, time.Now().Unix())
	return e
}
