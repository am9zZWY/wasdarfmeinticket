package cache

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestCache(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "cache.json")
	cache := Open(path)
	now := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
	cache.now = func() time.Time { return now }

	var got []string
	if cache.Get("zones", &got) {
		t.Fatal("empty cache returned a value")
	}
	cache.Set("zones", []string{"11", "220"}, time.Hour)
	cache.Set("short", "x", time.Minute)
	if !cache.Get("zones", &got) || len(got) != 2 || got[1] != "220" {
		t.Errorf("got %v", got)
	}

	// A second process sees what was stored.
	other := Open(path)
	other.now = cache.now
	got = nil
	if !other.Get("zones", &got) || len(got) != 2 {
		t.Errorf("reopened cache: %v", got)
	}

	// Entries expire, in memory and when the file is loaded again.
	now = now.Add(30 * time.Minute)
	var text string
	if cache.Get("short", &text) {
		t.Error("expired entry returned")
	}
	if !cache.Get("zones", &got) {
		t.Error("entry expired too early")
	}
	cache.Set("other", 1, time.Hour) // rewriting the file drops expired entries
	if size(cache) != 2 {
		t.Errorf("len = %d, want the expired entry gone", size(cache))
	}

	// A value of another shape is a miss, not a panic.
	var number int
	if cache.Get("zones", &number) {
		t.Error("mismatching type returned as hit")
	}
}

func TestCacheIsForgiving(t *testing.T) {
	var none *Cache
	none.Set("k", 1, time.Hour)
	var value int
	if none.Get("k", &value) || size(none) != 0 {
		t.Error("nil cache stored something")
	}

	mem := Open("")
	mem.Set("k", 1, time.Hour)
	if !mem.Get("k", &value) || value != 1 {
		t.Error("memory cache lost its value")
	}

	// A broken file and an unwritable location are ignored.
	dir := t.TempDir()
	broken := filepath.Join(dir, "broken.json")
	os.WriteFile(broken, []byte("{not json"), 0o644)
	if cache := Open(broken); size(cache) != 0 {
		t.Error("broken file produced entries")
	}
	unwritable := Open(filepath.Join(broken, "below-a-file", "cache.json"))
	unwritable.Set("k", 2, time.Hour)
	if !unwritable.Get("k", &value) || value != 2 {
		t.Error("cache must keep working in memory when the file cannot be written")
	}
}

func TestCacheConcurrent(t *testing.T) {
	cache := Open(filepath.Join(t.TempDir(), "cache.json"))
	var waitGroup sync.WaitGroup
	for index := 0; index < 20; index++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			cache.Set("k", index, time.Hour)
			var value int
			cache.Get("k", &value)
		}()
	}
	waitGroup.Wait()
	if size(cache) != 1 {
		t.Errorf("len = %d", size(cache))
	}
}

// size returns the number of entries, expired ones included.
func size(cache *Cache) int {
	if cache == nil {
		return 0
	}
	cache.mutex.Lock()
	defer cache.mutex.Unlock()
	return len(cache.entries)
}
