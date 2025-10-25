package speedtest

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"

	"github.com/xiecang/speedtest-clash/speedtest/models"
)

var (
	// 单例缓存实例
	defaultCacheInstance models.Cache
	defaultCacheOnce     sync.Once
)

// DefaultCache 默认内存缓存实现
type DefaultCache struct {
	storage       sync.Map
	defaultTTL    time.Duration
	cleanupTicker *time.Ticker
	stopChan      chan struct{}
}

type cacheItem struct {
	result    *models.CProxyWithResult
	expiresAt time.Time
}

// GetDefaultCache 获取单例默认缓存
func GetDefaultCache() models.Cache {
	defaultCacheOnce.Do(func() {
		defaultCacheInstance = &DefaultCache{
			defaultTTL: 30 * time.Minute,
			stopChan:   make(chan struct{}),
		}
		// 启动清理协程
		defaultCacheInstance.(*DefaultCache).startCleanup()
	})
	return defaultCacheInstance
}

// NewDefaultCache 创建新的默认缓存实例（不推荐，建议使用 GetDefaultCache）
func NewDefaultCache() models.Cache {
	cache := &DefaultCache{
		defaultTTL: 30 * time.Minute,
		stopChan:   make(chan struct{}),
	}

	// 启动清理协程
	cache.startCleanup()

	return cache
}

// NewDefaultCacheWithTTL 创建指定TTL的默认缓存
func NewDefaultCacheWithTTL(ttl time.Duration) models.Cache {
	cache := &DefaultCache{
		defaultTTL: ttl,
		stopChan:   make(chan struct{}),
	}

	// 启动清理协程
	cache.startCleanup()

	return cache
}

func (c *DefaultCache) Get(ctx context.Context, key string) (*models.CProxyWithResult, bool) {
	if item, ok := c.storage.Load(key); ok {
		cacheItem := item.(*cacheItem)

		// 检查是否过期
		if cacheItem.expiresAt.IsZero() || time.Now().Before(cacheItem.expiresAt) {
			return cacheItem.result, true
		}

		// 过期了，删除
		c.storage.Delete(key)
	}

	return nil, false
}

func (c *DefaultCache) Set(ctx context.Context, key string, result *models.CProxyWithResult) error {
	item := &cacheItem{
		result:    result,
		expiresAt: time.Now().Add(c.defaultTTL),
	}

	c.storage.Store(key, item)
	return nil
}

func (c *DefaultCache) GenerateKey(proxy *models.CProxy) string {
	bytes, err := json.Marshal(proxy.SecretConfig)
	if err != nil {
		return proxy.Name()
	}
	hash := md5.Sum(bytes)
	md5Str := hex.EncodeToString(hash[:])
	return md5Str
}

// startCleanup 启动清理协程
func (c *DefaultCache) startCleanup() {
	c.cleanupTicker = time.NewTicker(c.defaultTTL)

	go func() {
		for {
			select {
			case <-c.cleanupTicker.C:
				c.cleanup()
			case <-c.stopChan:
				c.cleanupTicker.Stop()
				return
			}
		}
	}()
}

// cleanup 清理过期缓存
func (c *DefaultCache) cleanup() {
	now := time.Now()
	c.storage.Range(func(key, value interface{}) bool {
		item := value.(*cacheItem)
		if !item.expiresAt.IsZero() && now.After(item.expiresAt) {
			c.storage.Delete(key)
		}
		return true
	})
}

// Close 关闭缓存，停止清理协程
func (c *DefaultCache) Close() error {
	// 如果是单例缓存，不关闭清理协程
	if c == defaultCacheInstance {
		return nil
	}
	close(c.stopChan)
	return nil
}
