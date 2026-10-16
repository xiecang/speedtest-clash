// SPDX-License-Identifier: GPL-3.0-only
package filter

import (
	"crypto/sha256"
	"sync"
)

// Pure expression results need no TTL: the digest includes every input byte.
// Store no node data; FIFO eviction bounds memory across repeated sources.
var results = newResultCache(100000)

type resultCache struct {
	mu     sync.Mutex
	values map[[32]byte]bool
	keys   [][32]byte
	limit  int
	next   int
}

func newResultCache(limit int) *resultCache {
	return &resultCache{values: make(map[[32]byte]bool), limit: limit}
}

func resultKey(expression string, object []byte) [32]byte {
	h := sha256.New()
	h.Write([]byte(expression))
	h.Write([]byte{0})
	h.Write(object)
	var key [32]byte
	h.Sum(key[:0])
	return key
}

func (c *resultCache) get(key [32]byte) (bool, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	value, ok := c.values[key]
	return value, ok
}

func (c *resultCache) put(key [32]byte, value bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.values[key]; ok {
		return
	}
	if len(c.keys) < c.limit {
		c.keys = append(c.keys, key)
	} else {
		delete(c.values, c.keys[c.next])
		c.keys[c.next] = key
		c.next = (c.next + 1) % c.limit
	}
	c.values[key] = value
}
