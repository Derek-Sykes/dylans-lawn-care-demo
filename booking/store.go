package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type Store struct {
	db        *sql.DB
	key       []byte
	aead      cipher.AEAD
	installID string
}

func randomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("cryptographic randomness unavailable")
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func openStore(dir, bootstrap string) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	keyPath := filepath.Join(dir, "encryption.key")
	key, err := os.ReadFile(keyPath)
	if errors.Is(err, os.ErrNotExist) {
		// Refuse a fresh key beside an existing DB: losing a key must not silently
		// overwrite or detach the existing owner's encrypted credentials.
		if _, e := os.Stat(filepath.Join(dir, "booking.sqlite")); e == nil {
			return nil, errors.New("encryption key is missing; restore the matching database and key backup")
		}
		key = make([]byte, 32)
		if _, err = rand.Read(key); err != nil {
			return nil, err
		}
		f, e := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return nil, e
		}
		_, err = f.Write(key)
		if err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err == nil {
			err = closeErr
		}
	}
	if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, errors.New("invalid encryption key")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	dbPath := filepath.Join(dir, "booking.sqlite")
	db, err := sql.Open("sqlite3", dbPath+"?_busy_timeout=10000&_journal_mode=WAL&_foreign_keys=on&_synchronous=FULL&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, key: key, aead: aead}
	if err = s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	_ = os.Chmod(dbPath, 0600)
	if err = s.getJSON("install_id", &s.installID); errors.Is(err, sql.ErrNoRows) {
		s.installID = hex.EncodeToString([]byte(randomToken(12)))[:20]
		err = s.putJSON("install_id", s.installID)
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	var oldDigest string
	err = s.getJSON("bootstrap_digest", &oldDigest)
	if errors.Is(err, sql.ErrNoRows) {
		err = s.putJSON("bootstrap_digest", s.hash(bootstrap))
	} else if err == nil && !constantEqual(oldDigest, s.hash(bootstrap)) {
		err = errors.New("bootstrap credential does not match the persistent installation")
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	var settings Settings
	if err = s.getJSON("settings", &settings); errors.Is(err, sql.ErrNoRows) {
		err = s.putJSON("settings", defaultSettings())
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY,value BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS secrets (key TEXT PRIMARY KEY,value BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS sessions (id TEXT PRIMARY KEY,value BLOB NOT NULL,expires INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS oauth_states (id TEXT PRIMARY KEY,binding TEXT NOT NULL,value BLOB NOT NULL,expires INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS bookings (
 id TEXT PRIMARY KEY,idempotency_key TEXT NOT NULL UNIQUE,payload_hash TEXT NOT NULL,
 start INTEGER NOT NULL,end INTEGER NOT NULL,blocked_end INTEGER NOT NULL,
 service_id TEXT NOT NULL,name TEXT NOT NULL,email TEXT NOT NULL,phone TEXT NOT NULL,address TEXT NOT NULL,notes TEXT NOT NULL,
 status TEXT NOT NULL DEFAULT 'needs_followup',calendar_status TEXT NOT NULL DEFAULT 'pending',
 created_at INTEGER NOT NULL,admin_notes TEXT NOT NULL DEFAULT '',event_id TEXT NOT NULL UNIQUE,
 generation INTEGER NOT NULL DEFAULT 1,sync_generation INTEGER NOT NULL DEFAULT 0,attempts INTEGER NOT NULL DEFAULT 0,next_attempt INTEGER NOT NULL DEFAULT 0,
 CHECK(end>start),CHECK(blocked_end>=end));
CREATE INDEX IF NOT EXISTS booking_overlap ON bookings(start,blocked_end);
CREATE INDEX IF NOT EXISTS booking_jobs ON bookings(calendar_status,next_attempt);
CREATE TRIGGER IF NOT EXISTS prevent_booking_overlap BEFORE INSERT ON bookings
WHEN EXISTS(SELECT 1 FROM bookings b WHERE (b.status!='cancelled' OR b.calendar_status!='synced') AND NEW.start<b.blocked_end AND NEW.blocked_end>b.start)
BEGIN SELECT RAISE(ABORT,'booking_overlap'); END;`)
	return err
}

func (s *Store) hash(value string) string {
	h := hmac.New(sha256.New, s.key)
	h.Write([]byte(value))
	return hex.EncodeToString(h.Sum(nil))
}
func constantEqual(a, b string) bool { return hmac.Equal([]byte(a), []byte(b)) }
func (s *Store) seal(data []byte, purpose string) ([]byte, error) {
	n := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(n); err != nil {
		return nil, err
	}
	return s.aead.Seal(n, n, data, []byte(purpose)), nil
}
func (s *Store) open(data []byte, purpose string) ([]byte, error) {
	if len(data) < s.aead.NonceSize() {
		return nil, errors.New("invalid encrypted record")
	}
	return s.aead.Open(nil, data[:s.aead.NonceSize()], data[s.aead.NonceSize():], []byte(purpose))
}
func (s *Store) putJSON(key string, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = s.db.Exec("INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, b)
	return err
}
func (s *Store) getJSON(key string, value any) error {
	var b []byte
	if err := s.db.QueryRow("SELECT value FROM meta WHERE key=?", key).Scan(&b); err != nil {
		return err
	}
	return json.Unmarshal(b, value)
}
func (s *Store) putSecret(key string, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	b, err = s.seal(b, key)
	if err != nil {
		return err
	}
	_, err = s.db.Exec("INSERT INTO secrets(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, b)
	return err
}
func (s *Store) getSecret(key string, value any) error {
	var b []byte
	if err := s.db.QueryRow("SELECT value FROM secrets WHERE key=?", key).Scan(&b); err != nil {
		return err
	}
	b, err := s.open(b, key)
	if err != nil {
		return fmt.Errorf("cannot decrypt %s record", key)
	}
	return json.Unmarshal(b, value)
}
func (s *Store) settings() (Settings, error) {
	var v Settings
	err := s.getJSON("settings", &v)
	return canonicalSettings(v), err
}
func (s *Store) close() error { return s.db.Close() }
func (s *Store) cleanup(now time.Time) {
	_, _ = s.db.Exec("DELETE FROM sessions WHERE expires<?", now.Unix())
	_, _ = s.db.Exec("DELETE FROM oauth_states WHERE expires<?", now.Unix())
}
