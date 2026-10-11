package main

// Short-lived cache of API answers: many agents ask for the same live
// scores and tables at once. Concurrent misses for one URL share a request.

import (
	"strings"
	"sync"
	"time"
)

type cacheEntry struct {
	body    []byte
	expires time.Time
}

type call struct {
	wg   sync.WaitGroup
	body []byte
	err  error
}

var (
	cacheMu  sync.Mutex
	cache    = map[string]cacheEntry{}
	inflight = map[string]*call{}
)

const maxCacheEntries = 3000

// cacheTTL: live data briefly, profiles and search longer
func cacheTTL(path string) time.Duration {
	switch {
	case strings.HasPrefix(path, "fixtures/feed_livenow"), strings.HasPrefix(path, "matches/"):
		return 15 * time.Second
	case strings.HasPrefix(path, "fixtures/"):
		return 30 * time.Second
	case strings.HasPrefix(path, "fixtures_v2/"), strings.HasPrefix(path, "tables/"):
		return time.Minute
	case strings.HasPrefix(path, "h2h/"):
		return time.Hour
	}
	return 10 * time.Minute // team_gs, players, topscorers, search
}

// cached returns the body for key, fetching it at most once at a time
func cached(key string, ttl time.Duration, fetch func() ([]byte, error)) ([]byte, error) {
	cacheMu.Lock()
	if e, ok := cache[key]; ok && time.Now().Before(e.expires) {
		cacheMu.Unlock()
		return e.body, nil
	}
	if c, ok := inflight[key]; ok {
		cacheMu.Unlock()
		c.wg.Wait()
		return c.body, c.err
	}
	c := &call{}
	c.wg.Add(1)
	inflight[key] = c
	cacheMu.Unlock()

	c.body, c.err = fetch()

	cacheMu.Lock()
	delete(inflight, key)
	if c.err == nil {
		if len(cache) >= maxCacheEntries {
			pruneCache()
		}
		cache[key] = cacheEntry{c.body, time.Now().Add(ttl)}
	}
	cacheMu.Unlock()
	c.wg.Done()
	return c.body, c.err
}

// pruneCache drops expired entries, or everything when none expired;
// called with cacheMu held
func pruneCache() {
	now := time.Now()
	for k, e := range cache {
		if now.After(e.expires) {
			delete(cache, k)
		}
	}
	if len(cache) >= maxCacheEntries {
		cache = map[string]cacheEntry{}
	}
}
