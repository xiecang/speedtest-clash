package speedtest

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
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

func cacheKey(proxy *models.CProxy) string {
	bytes, err := json.Marshal(proxy.SecretConfig)
	if err != nil {
		return proxy.Proxy.Name()
	}
	hash := md5.Sum(bytes)
	md5Str := hex.EncodeToString(hash[:])
	return md5Str
}

func addResultCache(result *models.CProxyWithResult) {
	key := cacheKey(&result.Proxy)
	resultCaches.Store(key, resultCache{
		result:    result,
		cacheTime: time.Now(),
	})
}

func getCacheFromResult(key string) *models.CProxyWithResult {
	if cache, ok := resultCaches.Load(key); ok {
		r := cache.(resultCache)
		if time.Since(r.cacheTime) < cacheTimeout {
			return r.result
		} else {
			resultCaches.Delete(key)
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
