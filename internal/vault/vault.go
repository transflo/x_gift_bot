package vault

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"golang.org/x/crypto/scrypt"
)

type Vault struct {
	db   *sql.DB
	aead cipher.AEAD
}

func NewPassword() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	f, err := os.CreateTemp("/tmp", "xgift-password-*")
	if err != nil {
		return "", err
	}
	path := f.Name()
	if _, err = f.WriteString(base64.RawURLEncoding.EncodeToString(b) + "\n"); err != nil {
		f.Close()
		os.Remove(path)
		return "", err
	}
	if err = f.Close(); err != nil {
		return "", err
	}
	return path, nil
}

func Open(path, passwordFile string, create bool) (*Vault, error) {
	info, err := os.Lstat(passwordFile)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("password file must be a regular owner-only file (0600)")
	}
	password, err := os.ReadFile(passwordFile)
	if err != nil {
		return nil, err
	}
	password = []byte(strings.TrimSpace(string(password)))
	defer clear(password)
	if len(password) < 32 {
		return nil, errors.New("password is too short")
	}
	if create {
		if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return nil, err
		}
		if err = os.Chmod(filepath.Dir(path), 0700); err != nil {
			return nil, err
		}
		f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return nil, e
		}
		f.Close()
	}
	info, err = os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("database must be a regular owner-only file")
	}
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
	var salt []byte
	if create {
		_, err = db.Exec(`CREATE TABLE metadata (key TEXT PRIMARY KEY, value BLOB NOT NULL); CREATE TABLE secrets (name TEXT PRIMARY KEY, payload BLOB NOT NULL, created INTEGER NOT NULL DEFAULT 0);`)
		if err != nil {
			return nil, err
		}
		salt = make([]byte, 32)
		if _, err = rand.Read(salt); err != nil {
			return nil, err
		}
		_, err = db.Exec("INSERT INTO metadata VALUES ('salt', ?)", salt)
		if err != nil {
			return nil, err
		}
	} else {
		if err = db.QueryRow("SELECT value FROM metadata WHERE key='salt'").Scan(&salt); err != nil {
			return nil, err
		}
		if err = migrateSecrets(db); err != nil {
			return nil, err
		}
	}
	if len(salt) != 32 {
		return nil, errors.New("invalid vault salt")
	}
	key, err := scrypt.Key(password, salt, 32768, 8, 1, 32)
	if err != nil {
		return nil, err
	}
	defer clear(key)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	v := &Vault{db, aead}
	if create {
		err = v.Put("vault-check", []byte("xgift-v1"))
	} else {
		var b []byte
		b, err = v.Get("vault-check")
		if err == nil && string(b) != "xgift-v1" {
			err = errors.New("invalid vault version")
		}
	}
	if err != nil {
		return nil, errors.New("vault authentication failed (wrong password or corrupted database)")
	}
	ok = true
	return v, nil
}

func (v *Vault) Put(name string, plain []byte) error {
	nonce := make([]byte, v.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	encrypted := v.aead.Seal(nonce, nonce, plain, []byte("xgift-v1:"+name))
	_, err := v.db.Exec("INSERT INTO secrets(name,payload,created) VALUES (?,?,?) ON CONFLICT(name) DO UPDATE SET payload=excluded.payload", name, encrypted, time.Now().Unix())
	return err
}
func (v *Vault) Get(name string) ([]byte, error) {
	var b []byte
	if err := v.db.QueryRow("SELECT payload FROM secrets WHERE name=?", name).Scan(&b); err != nil {
		return nil, fmt.Errorf("secret %s unavailable: %w", name, err)
	}
	n := v.aead.NonceSize()
	if len(b) < n {
		return nil, errors.New("invalid encrypted record")
	}
	return v.aead.Open(nil, b[:n], b[n:], []byte("xgift-v1:"+name))
}

// PutIfAbsent makes first-writer selection durable across concurrent processes.
func (v *Vault) PutIfAbsent(name string, plain []byte) (bool, error) {
	nonce := make([]byte, v.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return false, err
	}
	encrypted := v.aead.Seal(nonce, nonce, plain, []byte("xgift-v1:"+name))
	result, err := v.db.Exec("INSERT INTO secrets(name,payload,created) VALUES (?,?,?) ON CONFLICT(name) DO NOTHING", name, encrypted, time.Now().Unix())
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}
func (v *Vault) Close() error { return v.db.Close() }

// migrateSecrets adds the record timestamp to a vault created before retention
// existed. Existing rows are stamped with the upgrade time rather than zero, so
// an upgrade can never delete records the operator has not had a chance to age
// out under the new policy.
func migrateSecrets(db *sql.DB) error {
	rows, err := db.Query("PRAGMA table_info(secrets)")
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var cid, required, primary int
		var name, typ string
		var defaultValue any
		if err = rows.Scan(&cid, &name, &typ, &required, &defaultValue, &primary); err != nil {
			rows.Close()
			return err
		}
		if name == "created" {
			found = true
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if found {
		return nil
	}
	if _, err = db.Exec("ALTER TABLE secrets ADD COLUMN created INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	_, err = db.Exec("UPDATE secrets SET created=? WHERE created=0", time.Now().Unix())
	return err
}

// PrunePrefixes deletes records under the given name prefixes that were written
// before the cutoff. It reports how many rows were removed. A prefix must end in
// a separator so "checkout-verification:" can never match "checkout-verificationx".
func (v *Vault) PrunePrefixes(cutoff int64, prefixes ...string) (int, error) {
	removed := 0
	for _, prefix := range prefixes {
		if !strings.HasSuffix(prefix, ":") {
			return removed, errors.New("prune prefix must end in a colon")
		}
		result, err := v.db.Exec("DELETE FROM secrets WHERE name >= ? AND name < ? AND created > 0 AND created < ?", prefix, prefix+"\xff", cutoff)
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

// CountPrefixes reports record counts and approximate encrypted payload bytes
// per prefix, so the admin UI can show what is actually taking up space.
func (v *Vault) CountPrefixes(prefixes ...string) (map[string]PrefixUsage, error) {
	out := make(map[string]PrefixUsage, len(prefixes))
	for _, prefix := range prefixes {
		var usage PrefixUsage
		err := v.db.QueryRow("SELECT COUNT(*),COALESCE(SUM(LENGTH(payload)),0) FROM secrets WHERE name >= ? AND name < ?", prefix, prefix+"\xff").Scan(&usage.Records, &usage.Bytes)
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
func (v *Vault) Compact() (bool, error) {
	var freelist, pageCount, pageSize int64
	if err := v.db.QueryRow("PRAGMA freelist_count").Scan(&freelist); err != nil {
		return false, err
	}
	if err := v.db.QueryRow("PRAGMA page_count").Scan(&pageCount); err != nil {
		return false, err
	}
	if err := v.db.QueryRow("PRAGMA page_size").Scan(&pageSize); err != nil {
		return false, err
	}
	if pageCount == 0 || freelist*4 < pageCount || freelist*pageSize < 4<<20 {
		return false, nil
	}
	_, err := v.db.Exec("VACUUM")
	return err == nil, err
}

// Archive atomically preserves an authenticated record under a new name and removes
// its active key, only if its contents still match the verified snapshot.
func (v *Vault) Archive(name, archive string, expected, archived []byte) error {
	tx, err := v.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var payload []byte
	if err = tx.QueryRow("SELECT payload FROM secrets WHERE name=?", name).Scan(&payload); err != nil {
		return err
	}
	n := v.aead.NonceSize()
	if len(payload) < n {
		return errors.New("invalid encrypted record")
	}
	plain, err := v.aead.Open(nil, payload[:n], payload[n:], []byte("xgift-v1:"+name))
	if err != nil {
		return err
	}
	defer clear(plain)
	if !bytes.Equal(plain, expected) {
		return errors.New("order changed since verification")
	}
	nonce := make([]byte, n)
	if _, err = rand.Read(nonce); err != nil {
		return err
	}
	encrypted := v.aead.Seal(nonce, nonce, archived, []byte("xgift-v1:"+archive))
	if _, err = tx.Exec("INSERT INTO secrets(name,payload,created) VALUES (?,?,?)", archive, encrypted, time.Now().Unix()); err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM secrets WHERE name=?", name); err != nil {
		return err
	}
	return tx.Commit()
}

// ReplaceArchived atomically preserves the old checkout and installs a replacement.
// A crash can never leave a submitted order without its active record or audit.
func (v *Vault) ReplaceArchived(name, archive string, expected, proof, replacement []byte) error {
	tx, err := v.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var payload []byte
	if err = tx.QueryRow("SELECT payload FROM secrets WHERE name=?", name).Scan(&payload); err != nil {
		return err
	}
	n := v.aead.NonceSize()
	if len(payload) < n {
		return errors.New("invalid encrypted record")
	}
	plain, err := v.aead.Open(nil, payload[:n], payload[n:], []byte("xgift-v1:"+name))
	if err != nil {
		return err
	}
	defer clear(plain)
	if !bytes.Equal(plain, expected) {
		return errors.New("order changed since verification")
	}
	seal := func(key string, b []byte) ([]byte, error) {
		nonce := make([]byte, n)
		if _, e := rand.Read(nonce); e != nil {
			return nil, e
		}
		return v.aead.Seal(nonce, nonce, b, []byte("xgift-v1:"+key)), nil
	}
	saved, err := seal(archive, proof)
	if err != nil {
		return err
	}
	next, err := seal(name, replacement)
	if err != nil {
		return err
	}
	if _, err = tx.Exec("INSERT INTO secrets(name,payload,created) VALUES (?,?,?)", archive, saved, time.Now().Unix()); err != nil {
		return err
	}
	if _, err = tx.Exec("UPDATE secrets SET payload=? WHERE name=?", next, name); err != nil {
		return err
	}
	return tx.Commit()
}
