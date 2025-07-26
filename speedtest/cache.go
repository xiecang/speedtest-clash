package speedtest

import (
	"github.com/xiecang/speedtest-clash/speedtest/models"
	"sync"
	"time"
)

var (
	resultCaches = sync.Map{}
	cacheTimeout = 30 * time.Minute
)

func SetCacheTimeout(t time.Duration) {
	cacheTimeout = t
}

type resultCache struct {
	result    *models.CProxyWithResult
	cacheTime time.Time
}

func addResultCache(result *models.CProxyWithResult) {
	resultCaches.Store(result.Name, resultCache{
		result:    result,
		cacheTime: time.Now(),
	})
}

func getCacheFromResult(name string) *models.CProxyWithResult {
	if cache, ok := resultCaches.Load(name); ok {
		r := cache.(resultCache)
		if time.Since(r.cacheTime) < cacheTimeout {
			return r.result
		} else {
			resultCaches.Delete(name)
		}
	}
	return nil
}

func clearResultCache() {
	resultCaches.Range(func(key, value interface{}) bool {
		if time.Since(value.(resultCache).cacheTime) > cacheTimeout {
			resultCaches.Delete(key)
		}
		return true
	})
}

func init() {
	go func() {
		t := time.NewTicker(cacheTimeout)
		select {
		case <-t.C:
			clearResultCache()
		}
	}()
}
