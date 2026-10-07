// Package store keeps the named records the service holds outside its
// relational tables: the account pool, published configuration, checkout
// records and the audit trail around them.
//
// Payloads are stored as they are handed in. The database file is the only
// protection, so it is created owner-only and deleted pages are scrubbed.
package store

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type Store struct {
	db *sql.DB
}

// Open returns the record store at path, creating it when missing.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("record store must be a regular owner-only file")
	}
	// secure_delete scrubs the pages of pruned rows: the payloads are readable
	// as they sit, so a deleted row must not stay recoverable from the file.
	db, err := sql.Open("sqlite3", path+"?_busy_timeout=5000&_journal_mode=DELETE&_secure_delete=on&_synchronous=FULL")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	ok := false
	defer func() {
		if !ok {
			db.Close()
		}
	}()
	if _, err = db.Exec("CREATE TABLE IF NOT EXISTS records (name TEXT PRIMARY KEY, payload BLOB NOT NULL, created INTEGER NOT NULL DEFAULT 0)"); err != nil {
		return nil, err
	}
	ok = true
	return &Store{db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Put writes a record. Overwriting keeps the original creation time so the
// retention clock measures how long the record has existed, not how long since
// its last write.
func (s *Store) Put(name string, payload []byte) error {
	_, err := s.db.Exec("INSERT INTO records(name,payload,created) VALUES (?,?,?) ON CONFLICT(name) DO UPDATE SET payload=excluded.payload", name, payload, time.Now().Unix())
	return err
}

func (s *Store) Get(name string) ([]byte, error) {
	var b []byte
	if err := s.db.QueryRow("SELECT payload FROM records WHERE name=?", name).Scan(&b); err != nil {
		return nil, fmt.Errorf("record %s unavailable: %w", name, err)
	}
	return b, nil
}

// PrunePrefixes deletes records under the given name prefixes that were written
// before the cutoff. It reports how many rows were removed. A prefix must end in
// a separator so "checkout-verification:" can never match "checkout-verificationx".
func (s *Store) PrunePrefixes(cutoff int64, prefixes ...string) (int, error) {
	removed := 0
	for _, prefix := range prefixes {
		if !strings.HasSuffix(prefix, ":") {
			return removed, errors.New("prune prefix must end in a colon")
		}
		result, err := s.db.Exec("DELETE FROM records WHERE name >= ? AND name < ? AND created > 0 AND created < ?", prefix, prefix+"\xff", cutoff)
		if err != nil {
			return removed, err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return removed, err
		}
		removed += int(n)
	}
	return removed, nil
}

// CountPrefixes reports record counts and payload bytes per prefix, so the admin
// UI can show what is actually taking up space.
func (s *Store) CountPrefixes(prefixes ...string) (map[string]PrefixUsage, error) {
	out := make(map[string]PrefixUsage, len(prefixes))
	for _, prefix := range prefixes {
		var usage PrefixUsage
		err := s.db.QueryRow("SELECT COUNT(*),COALESCE(SUM(LENGTH(payload)),0) FROM records WHERE name >= ? AND name < ?", prefix, prefix+"\xff").Scan(&usage.Records, &usage.Bytes)
		if err != nil {
			return nil, err
		}
		out[prefix] = usage
	}
	return out, nil
}

// PrefixUsage is one bucket of the storage report.
type PrefixUsage struct {
	Records int   `json:"records"`
	Bytes   int64 `json:"bytes"`
}

// Compact reclaims space after pruning. VACUUM rewrites the whole file, so it
// only runs once the freelist is a meaningful share of the database.
func (s *Store) Compact() (bool, error) {
	var freelist, pageCount, pageSize int64
	if err := s.db.QueryRow("PRAGMA freelist_count").Scan(&freelist); err != nil {
		return false, err
	}
	if err := s.db.QueryRow("PRAGMA page_count").Scan(&pageCount); err != nil {
		return false, err
	}
	if err := s.db.QueryRow("PRAGMA page_size").Scan(&pageSize); err != nil {
		return false, err
	}
	if pageCount == 0 || freelist*4 < pageCount || freelist*pageSize < 4<<20 {
		return false, nil
	}
	_, err := s.db.Exec("VACUUM")
	return err == nil, err
}

// Archive atomically preserves an verified record under a new name and removes
// its active key, only if its contents still match the verified snapshot.
func (s *Store) Archive(name, archive string, expected, archived []byte) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var payload []byte
	if err = tx.QueryRow("SELECT payload FROM records WHERE name=?", name).Scan(&payload); err != nil {
		return err
	}
	if !bytes.Equal(payload, expected) {
		return errors.New("order changed since verification")
	}
	if _, err = tx.Exec("INSERT INTO records(name,payload,created) VALUES (?,?,?)", archive, archived, time.Now().Unix()); err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM records WHERE name=?", name); err != nil {
		return err
	}
	return tx.Commit()
}
