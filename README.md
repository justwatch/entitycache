# entitycache

A simple entity in-mem synchronised cache with TTL, TTR and LFU policies, based on Ristretto.

# Basic usage

```go
// Declares a new entity cache limited to 10000 entries, with a 5 minutes time to refresh and a 10 minutes time to live.
// It exposes a Prometheus metric named "entity_cache_hit_total" with the labels "namespace" and "hit".
cache, err := entitycache.New(10000, 5*time.Minute, 10*time.Minute, "entity_cache_hit_total")
if err != nil {
    panic(err)
}

// Let's say we want to cache the document with the ID 128.
documentID := 128

document, ok, err := cache.Get(ctx, "documents", fmt.Sprintf("%d", documentID), func(ctx context.Context) (value interface{}, ok bool, err error) {
    document, err := getDocumentByID(ctx, documentID)
    if err != nil {
        if err == sql.ErrNoRows {
            return nil, false, nil
        }
        return nil, false, err
    }
    return document, true, nil
})
```

# Advanced usage

In some (rare) cases, we may want to cache additional keys when caching.

```go
// Declares a new entity cache limited to 10000 entries, with a 5 minutes time to refresh and a 10 minutes time to live.
// It exposes a Prometheus metric named "entity_cache_hit_total" with the labels "namespace" and "hit".
cache, err := entitycache.New(10000, 5*time.Minute, 10*time.Minute, "entity_cache_hit_total")
if err != nil {
    panic(err)
}

// Let's say we want to cache the document with the ID 128.
documentID := 128
// We also want it localized in french.
language := "fr"
// Our cache key would then be for example:
key := fmt.Sprintf("%d-%s", documentID, language)
// We additionally cache the document for an "any" key.
// All the languages being requested will keep this entry warm.
anyKey := fmt.Sprintf("%d-any", documentID)

// We get the french document from the cache.
document, ok, err := cache.MultiKeyGet(ctx, "documents", key, []string{anyKey}, func(ctx context.Context) (value interface{}, ok bool, err error) {
    if language == "any" {
        // We fallback to english by default.
        language = "en"
    }
    document, err := getDocumentByID(ctx, documentID, language)
    if err != nil {
        if err == sql.ErrNoRows {
            return nil, false, nil
        }
        return nil, false, err
    }
    return document, true, nil
})

// A next call asking for an "any" language would opportunistically get a cached document, in whatever language laying in the cache.
```
