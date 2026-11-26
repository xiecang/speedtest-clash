// SPDX-License-Identifier: GPL-3.0-only
package filter

import (
	"crypto/sha256"
	"encoding/binary"
	"io"
	"sync"
)

const resultCacheShards = 32

// Pure expression results need no TTL: the digest includes every input byte.
// Store no node data; FIFO eviction bounds memory across repeated sources.
var results = newResultCache(100000)

type resultShard struct {
	mu     sync.RWMutex
	values map[[32]byte]bool
	keys   [][32]byte
	limit  int
	next   int
}

type resultCache struct {
	shards [resultCacheShards]resultShard
	mask   byte
}

func newResultCache(limit int) *resultCache {
	c := &resultCache{}
	if limit <= 0 {
		return c
	}
	if limit < resultCacheShards {
		c.mask = 0
		c.shards[0] = resultShard{
			values: make(map[[32]byte]bool, limit),
			limit:  limit,
		}
		return c
	}
	c.mask = resultCacheShards - 1
	base := limit / resultCacheShards
	rem := limit % resultCacheShards
	for i := range c.shards {
		shardLimit := base
		if i < rem {
			shardLimit++
		}
		c.shards[i] = resultShard{
			values: make(map[[32]byte]bool),
			limit:  shardLimit,
		}
	}
	return c
}

func resultKey(expression string, object []byte) [32]byte {
	h := sha256.New()
	_, _ = io.WriteString(h, expression)
	var sep [1]byte
	_, _ = h.Write(sep[:])
	_, _ = h.Write(object)
	var key [32]byte
	h.Sum(key[:0])
	return key
}

func fastResultKey(expression, nodeKey, displayName string, hidden bool, tag, dialerProxy, detour string) [32]byte {
	h := sha256.New()
	_, _ = io.WriteString(h, expression)
	// [0, 1] cannot collide with resultKey's [0, '{'] JSON object prefix.
	marker := [3]byte{0, 1, 0}
	if hidden {
		marker[2] = 1
	}
	_, _ = h.Write(marker[:])
	writeLenPrefixed := func(s string) {
		var buf [4]byte
		binary.LittleEndian.PutUint32(buf[:], uint32(len(s)))
		_, _ = h.Write(buf[:])
		_, _ = io.WriteString(h, s)
	}
	writeLenPrefixed(nodeKey)
	writeLenPrefixed(displayName)
	writeLenPrefixed(tag)
	writeLenPrefixed(dialerProxy)
	writeLenPrefixed(detour)
	var key [32]byte
	h.Sum(key[:0])
	return key
}

func (c *resultCache) get(key [32]byte) (bool, bool) {
	shard := &c.shards[key[0]&c.mask]
	shard.mu.RLock()
	value, ok := shard.values[key]
	shard.mu.RUnlock()
	return value, ok
}

func (c *resultCache) put(key [32]byte, value bool) {
	shard := &c.shards[key[0]&c.mask]
	shard.mu.Lock()
	defer shard.mu.Unlock()
	if shard.limit <= 0 {
		return
	}
	if _, ok := shard.values[key]; ok {
		return
	}
	if len(shard.keys) < shard.limit {
		shard.keys = append(shard.keys, key)
	} else {
		delete(shard.values, shard.keys[shard.next])
		shard.keys[shard.next] = key
		shard.next = (shard.next + 1) % shard.limit
	}
	shard.values[key] = value
}

func (c *resultCache) size() int {
	total := 0
	for i := range c.shards {
		shard := &c.shards[i]
		shard.mu.RLock()
		total += len(shard.values)
		shard.mu.RUnlock()
	}
	return total
}
