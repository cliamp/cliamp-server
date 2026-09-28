package stats

import (
	"sync"
	"time"
)

const statsCacheTTL = time.Minute

// statsCache keeps aggregated results for statsCacheTTL. Concurrent misses for
// one key share a single query. A write during that query keeps the stale
// result out of the cache.
type statsCache[T any] struct {
	clone func(T) T

	mu         sync.Mutex
	entries    map[string]cacheEntry[T]
	generation map[string]uint64
	inflight   map[string]chan struct{}
}

type cacheEntry[T any] struct {
	result    T
	expiresAt time.Time
}

func newStatsCache[T any](clone func(T) T) *statsCache[T] {
	return &statsCache[T]{
		clone:      clone,
		entries:    make(map[string]cacheEntry[T]),
		generation: make(map[string]uint64),
		inflight:   make(map[string]chan struct{}),
	}
}

// get returns the cached result for key, or runs load and caches its result.
func (c *statsCache[T]) get(key string, load func() (T, error)) (T, error) {
	for {
		c.mu.Lock()
		if cached, ok := c.entries[key]; ok && time.Now().Before(cached.expiresAt) {
			result := c.clone(cached.result)
			c.mu.Unlock()
			return result, nil
		}
		if done, ok := c.inflight[key]; ok {
			c.mu.Unlock()
			<-done
			continue
		}
		generation := c.generation[key]
		done := make(chan struct{})
		c.inflight[key] = done
		c.mu.Unlock()

		result, err := load()

		c.mu.Lock()
		delete(c.inflight, key)
		if err == nil && c.generation[key] == generation {
			c.entries[key] = cacheEntry[T]{
				result:    c.clone(result),
				expiresAt: time.Now().Add(statsCacheTTL),
			}
		}
		close(done)
		c.mu.Unlock()
		return result, err
	}
}

// invalidate drops the cached result for key. A query that is in flight for
// key does not cache its result.
func (c *statsCache[T]) invalidate(key string) {
	c.mu.Lock()
	c.generation[key]++
	delete(c.entries, key)
	c.mu.Unlock()
}
