// Package cache keeps answers of the upstream services, in memory and
// optionally in a JSON file, so they survive restarts and are shared by CLI
// runs. Which zones a ride needs does not depend on the time of day, so
// the same route at another time can reuse an earlier answer.
package cache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Cache is a key/value store with expiry. A nil *Cache is valid and stores
// nothing.
type Cache struct {
	path string
	now  func() time.Time

	mutex   sync.Mutex
	entries map[string]entry
}

type entry struct {
	Expires time.Time       `json:"expires"`
	Value   json.RawMessage `json:"value"`
}

// Open returns a cache backed by the file at path, loading what is there.
// An empty path gives a memory-only cache. A missing or unreadable file is
// not an error: the cache starts empty.
func Open(path string) *Cache {
	cache := &Cache{path: path, now: time.Now, entries: map[string]entry{}}
	if path == "" {
		return cache
	}
	if data, err := os.ReadFile(path); err == nil {
		var stored map[string]entry
		// Expired entries are loaded as well: Get ignores them and the
		// next change drops them.
		if json.Unmarshal(data, &stored) == nil && stored != nil {
			cache.entries = stored
		}
	}
	return cache
}

// Get loads the value stored under key into out and reports whether a
// current entry was found.
func (cache *Cache) Get(key string, out any) bool {
	if cache == nil {
		return false
	}
	cache.mutex.Lock()
	item, ok := cache.entries[key]
	cache.mutex.Unlock()
	if !ok || !item.Expires.After(cache.now()) {
		return false
	}
	return json.Unmarshal(item.Value, out) == nil
}

// Set stores v under key for the given time. Failures to encode or to
// write the file are ignored: a cache must never break a check.
func (cache *Cache) Set(key string, value any, ttl time.Duration) {
	if cache == nil {
		return
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return
	}
	cache.mutex.Lock()
	defer cache.mutex.Unlock()
	cache.entries[key] = entry{Expires: cache.now().Add(ttl), Value: raw}
	cache.save()
}

// save writes the file atomically; the caller holds the lock.
func (cache *Cache) save() {
	if cache.path == "" {
		return
	}
	now := cache.now()
	for key, item := range cache.entries {
		if !item.Expires.After(now) {
			delete(cache.entries, key)
		}
	}
	data, err := json.Marshal(cache.entries)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(cache.path), 0o755); err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(cache.path), ".cache-*")
	if err != nil {
		return
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr != nil || cerr != nil || os.Rename(tmp.Name(), cache.path) != nil {
		os.Remove(tmp.Name())
	}
}
