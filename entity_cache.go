package entitycache

import (
	"context"
	"fmt"
	"time"

	"github.com/dgraph-io/ristretto"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/solher/locker"
)

var _ prometheus.Collector = &EntityCache{}

type EntityCache struct {
	promHitCounter *prometheus.CounterVec

	maxEntries int64
	ttr        time.Duration
	ttl        time.Duration
	cache      *ristretto.Cache
	locker     *locker.EntityLocker
}

// InMemOptions are caching options.
type InMemOptions struct {
}

func New(
	maxEntries int64,
	ttr time.Duration,
	ttl time.Duration,
	promMetricName string,
) (*EntityCache, error) {
	cache, err := ristretto.NewCache(&ristretto.Config{
		NumCounters: 10 * maxEntries, // Number of keys to track frequency of (recommended 10x the number of entries).
		MaxCost:     maxEntries,      // Maximum size of cache.
		BufferItems: 64,              // Number of keys per Get buffer. Default value.
	})
	if err != nil {
		return nil, err
	}
	return &EntityCache{
		promHitCounter: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: promMetricName,
				Help: "A counter of hit/miss on the entity cache.",
			},
			[]string{"method", "hit"},
		),

		maxEntries: maxEntries,
		ttr:        ttr,
		ttl:        ttl,

		cache:  cache,
		locker: locker.NewEntityLocker(),
	}, nil
}

func (sc *EntityCache) Get(
	ctx context.Context,
	namespace, key string,
	fetchFromSourceFunc func(ctx context.Context, getFromCache CacheGetter, setToCache CacheSetter) (value interface{}, ok bool, err error),
) (interface{}, bool, error) {
	cached, needsRefresh, hit := sc.getFromCache(buildCacheKey(namespace, key))
	switch {
	case hit && !needsRefresh:
		// If no error and we have fresh cached result, we return.
		sc.promHitCounter.With(prometheus.Labels{
			"method": namespace,
			"hit":    "true",
		}).Inc()

	case hit && needsRefresh:
		// If no error but we have stale cached result, we refresh async and return.
		sc.runAsync(ctx, func(ctx context.Context) error {
			_, _, err := sc.getAndCacheSource(ctx, namespace, key, fetchFromSourceFunc, false)
			return err
		})
		sc.promHitCounter.With(prometheus.Labels{
			"method": namespace,
			"hit":    "true",
		}).Inc()

	default:
		// If no error and no cached result, we fetch from the source and refresh the cache.
		return sc.getAndCacheSource(ctx, namespace, key, fetchFromSourceFunc, true)
	}
	return cached, true, nil
}

func (sc *EntityCache) getAndCacheSource(
	ctx context.Context,
	namespace, key string,
	fetchFromSourceFunc func(ctx context.Context, getFromCache CacheGetter, setToCache CacheSetter) (value interface{}, ok bool, err error),
	exportMetrics bool,
) (interface{}, bool, error) {
	cacheKey := buildCacheKey(namespace, key)

	// We take the write lock.
	sc.locker.Lock(cacheKey)

	// After taking the write lock, we recheck that the initial condition is still correct.
	cached, _, hit := sc.getFromCache(cacheKey)

	// If yes (i.e. we still need to refetch the data), we do actually refetch the data and unlock.
	if !hit {
		value, ok, err := fetchFromSourceFunc(
			ctx,
			func(key string) (value interface{}, ok bool) {
				v, ok, _ := sc.getFromCache(buildCacheKey(namespace, key))
				return v, ok
			},
			func(key string, value interface{}) {
				sc.setToCache(buildCacheKey(namespace, key), value)
			},
		)
		if ok {
			sc.setToCache(cacheKey, value)
		}
		sc.locker.Unlock(cacheKey)

		if exportMetrics {
			sc.promHitCounter.With(prometheus.Labels{
				"method": namespace,
				"hit":    "false",
			}).Inc()
		}
		return value, ok, err
	}

	// If no, we directly unlock and return what we found.
	sc.locker.Unlock(cacheKey)

	if exportMetrics {
		sc.promHitCounter.With(prometheus.Labels{
			"method": namespace,
			"hit":    "true",
		}).Inc()
	}
	return cached, true, nil
}

type timestampedCacheValue struct {
	NeedsRefreshAt time.Time
	Value          interface{}
}

func (sc *EntityCache) getFromCache(cacheKey string) (value interface{}, needsRefresh, ok bool) {
	val, ok := sc.cache.Get(cacheKey)
	if !ok {
		return nil, false, false
	}
	tsVal := val.(timestampedCacheValue)

	return tsVal.Value, time.Now().After(tsVal.NeedsRefreshAt), true
}

func (sc *EntityCache) setToCache(cacheKey string, value interface{}) {
	v := timestampedCacheValue{
		NeedsRefreshAt: time.Now().Add(sc.ttr),
		Value:          value,
	}
	sc.cache.SetWithTTL(cacheKey, v, 1, sc.ttl)
}

// runAsync runs a job in the background.
func (sc *EntityCache) runAsync(ctx context.Context, job func(ctx context.Context) error) {
	ctx = detach(ctx)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)

	go func() {
		_ = job(ctx)
		cancel()
	}()
}

// Describe implements prometheus.Collector.
func (sc *EntityCache) Describe(ch chan<- *prometheus.Desc) {
	sc.promHitCounter.Describe(ch)
}

// Collect implements prometheus.Collector.
func (sc *EntityCache) Collect(ch chan<- prometheus.Metric) {
	sc.promHitCounter.Collect(ch)
}

func buildCacheKey(namespace, key string) string {
	return fmt.Sprintf("%s|%s", namespace, key)
}

// CacheGetter is a function that returns a value from the cache.
type CacheGetter func(key string) (value interface{}, ok bool)

// CacheSetter is a function that sets a value in the cache.
type CacheSetter func(key string, value interface{})

// detach returns a context that keeps all the values of its parent context
// but detaches from the cancellation and error handling.
// Taken from: https://github.com/golang/tools/blob/master/internal/xcontext/xcontext.go
func detach(ctx context.Context) context.Context { return detachedContext{ctx} }

type detachedContext struct{ parent context.Context }

func (v detachedContext) Deadline() (time.Time, bool)       { return time.Time{}, false }
func (v detachedContext) Done() <-chan struct{}             { return nil }
func (v detachedContext) Err() error                        { return nil }
func (v detachedContext) Value(key interface{}) interface{} { return v.parent.Value(key) }
