// SPDX-License-Identifier: AGPL-3.0-only

// Package store wraps Wargo's SQLite database.
package store

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

type Store struct {
	DB  *sql.DB
	key []byte
}

var migrations = []string{
	`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
	CREATE TABLE sessions (
		id_hash TEXT PRIMARY KEY, csrf TEXT NOT NULL, created_at INTEGER NOT NULL,
		last_seen INTEGER NOT NULL, expires_at INTEGER NOT NULL, ip TEXT, ua TEXT,
		method TEXT NOT NULL);
	CREATE TABLE webauthn_credentials (
		id TEXT PRIMARY KEY, name TEXT NOT NULL, data TEXT NOT NULL, rp_id TEXT NOT NULL,
		created_at INTEGER NOT NULL, last_used INTEGER);
	CREATE TABLE recovery_codes (hash TEXT PRIMARY KEY, used_at INTEGER);
	CREATE TABLE audit_log (
		id INTEGER PRIMARY KEY AUTOINCREMENT, ts INTEGER NOT NULL, ip TEXT, ua TEXT,
		action TEXT NOT NULL, target TEXT, result TEXT NOT NULL, elevation TEXT, details TEXT);
	CREATE INDEX audit_ts ON audit_log(ts);
	CREATE TABLE jobs (
		id TEXT PRIMARY KEY, kind TEXT NOT NULL, title TEXT NOT NULL, state TEXT NOT NULL,
		started_at INTEGER NOT NULL, finished_at INTEGER, unit TEXT, log_path TEXT, result TEXT);
	CREATE TABLE launcher_tiles (
		id TEXT PRIMARY KEY, name TEXT NOT NULL, url TEXT NOT NULL, icon TEXT,
		group_name TEXT, sort INTEGER NOT NULL DEFAULT 0, new_tab INTEGER NOT NULL DEFAULT 1,
		health_url TEXT, enabled INTEGER NOT NULL DEFAULT 1);`,
	// "Keep me signed in" sessions, and the browser each session belongs to
	// (a new sign-in replaces that browser's older session).
	`ALTER TABLE sessions ADD COLUMN remember INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE sessions ADD COLUMN device TEXT;
	CREATE INDEX sessions_device ON sessions(device);`,
	// Notifications: Recent items in the panel, and the devices that
	// receive Web Push (each registration belongs to a session).
	`CREATE TABLE notify_items (
		id TEXT PRIMARY KEY, category TEXT NOT NULL, key TEXT NOT NULL,
		title TEXT NOT NULL, body TEXT, link TEXT, created_at INTEGER NOT NULL,
		resolved_at INTEGER, cleared_at INTEGER);
	CREATE INDEX notify_items_cat_key ON notify_items(category, key);
	CREATE TABLE notify_subs (
		id TEXT PRIMARY KEY, session TEXT NOT NULL, endpoint TEXT NOT NULL UNIQUE,
		p256dh TEXT NOT NULL, auth TEXT NOT NULL, created_at INTEGER NOT NULL);`,
}

// Open opens (and migrates) the database in dataDir.
func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	// The directory holds the database (sessions, audit log) and the
	// secret key: keep it private even if it was created by hand.
	if st, err := os.Stat(dataDir); err == nil && st.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(dataDir, st.Mode().Perm()&0o700); err != nil {
			slog.Warn("data directory is accessible by other users", "dir", dataDir, "mode", st.Mode().Perm(), "err", err)
		}
	}
	key, err := loadKey(filepath.Join(dataDir, "secret.key"))
	if err != nil {
		return nil, err
	}
	dsn := "file:" + filepath.Join(dataDir, "wargo.db") +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{DB: db, key: key}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.DB.Close() }

func (s *Store) migrate() error {
	var v int
	if err := s.DB.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	for i := v; i < len(migrations); i++ {
		tx, err := s.DB.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// loadKey reads the instance key, creating it on first start. An existing
// key of the wrong size is an error, never silently replaced (that would
// make every sealed secret unreadable). Creation is atomic and exclusive,
// so concurrent starts (`wargo admin` while serving) agree on one key.
func loadKey(path string) ([]byte, error) {
	b, err := readKey(path)
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		return b, err
	}
	b = make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".secret-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return nil, err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	// link(2) fails if the key appeared meanwhile; then use that one.
	if err := os.Link(tmp.Name(), path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return readKey(path)
		}
		// Filesystems without hard links: exclusive create instead.
		f, cerr := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(cerr, os.ErrExist) {
			return readKey(path)
		}
		if cerr != nil {
			return nil, cerr
		}
		_, werr := f.Write(b)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			os.Remove(path)
			return nil, werr
		}
	}
	return b, nil
}

func readKey(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Mode().Perm()&0o077 != 0 {
		if err := f.Chmod(0o600); err != nil {
			return nil, fmt.Errorf("%s is readable by other users and can't be fixed: %w", path, err)
		}
	}
	b, err := io.ReadAll(io.LimitReader(f, 64))
	if err != nil {
		return nil, err
	}
	if len(b) != 32 {
		return nil, fmt.Errorf("%s is corrupt (%d bytes, want 32); restore it from a backup or remove it (stored integration secrets will be lost)", path, len(b))
	}
	return b, nil
}

// Get decodes the JSON setting key into v. Returns false if absent.
func (s *Store) Get(ctx context.Context, key string, v any) (bool, error) {
	var raw string
	err := s.DB.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal([]byte(raw), v)
}

// Set stores v as JSON under key.
func (s *Store) Set(ctx context.Context, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx,
		`INSERT INTO settings(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, string(b))
	return err
}

func (s *Store) Delete(ctx context.Context, key string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, key)
	return err
}

// Seal encrypts a secret with the instance key (AES-256-GCM).
func (s *Store) Seal(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	blk, err := aes.NewCipher(s.key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(blk)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(gcm.Seal(nonce, nonce, []byte(plain), nil)), nil
}

// Open decrypts a value produced by Seal.
func (s *Store) Unseal(sealed string) (string, error) {
	if sealed == "" {
		return "", nil
	}
	b, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		return "", err
	}
	blk, err := aes.NewCipher(s.key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(blk)
	if err != nil {
		return "", err
	}
	if len(b) < gcm.NonceSize() {
		return "", errors.New("sealed value too short")
	}
	out, err := gcm.Open(nil, b[:gcm.NonceSize()], b[gcm.NonceSize():], nil)
	return string(out), err
}
