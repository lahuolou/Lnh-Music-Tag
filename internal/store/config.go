// Package store provides the SQLite-backed configuration persistence used to
// store admin credentials, API keys and app options in the database instead of
// environment variables. No CGo: uses modernc.org/sqlite (pure Go).
package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"sync"

	_ "modernc.org/sqlite"
)

// Config is a tiny key/value settings store backed by SQLite. Values are also
// cached in memory for cheap reads. It is safe for concurrent use.
type Config struct {
	db    *sql.DB
	mu    sync.RWMutex
	cache map[string]string
}

// OpenConfig opens (creating if needed) the SQLite settings DB inside dir and
// loads all rows into memory.
func OpenConfig(dir string) (*Config, error) {
	if dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
	}
	path := filepath.Join(dir, "lnh.db")
	// 批量任务/扫描/日志会并发写库：WAL + busy_timeout 让并发写等待而非
	// 直接报 SQLITE_BUSY 失败（此前并发下会出现大量 database is locked）。
	// PRAGMA 通过 DSN 参数下发，保证连接池每个新连接都生效。
	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		return nil, err
	}
	c := &Config{db: db, cache: map[string]string{}}
	if err := c.reload(); err != nil {
		return nil, err
	}
	return c, nil
}

// DB exposes the underlying SQLite handle so the in-memory track store can
// persist scan results into the same database file (single connection, no
// cross-process locking issues).
func (c *Config) DB() *sql.DB { return c.db }

// Close releases the underlying database.
func (c *Config) Close() error { return c.db.Close() }

func (c *Config) reload() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	rows, err := c.db.Query(`SELECT key, value FROM settings`)
	if err != nil {
		return err
	}
	defer rows.Close()
	c.cache = map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return err
		}
		c.cache[k] = v
	}
	return rows.Err()
}

// Get returns the value for key and whether it is set.
func (c *Config) Get(key string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.cache[key]
	return v, ok
}

// GetDefault returns the stored value or def when unset.
func (c *Config) GetDefault(key, def string) string {
	if v, ok := c.Get(key); ok {
		return v
	}
	return def
}

// Set upserts a key/value pair (persisting to SQLite and updating the cache).
func (c *Config) Set(key, value string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := c.db.Exec(
		`INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
		key, value); err != nil {
		return err
	}
	c.cache[key] = value
	return nil
}

// All returns a snapshot of the whole settings map.
func (c *Config) All() map[string]string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[string]string, len(c.cache))
	for k, v := range c.cache {
		out[k] = v
	}
	return out
}
