package speedtest

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/xiecang/speedtest-clash/speedtest/models"
)

func TestTermination(t *testing.T) {
	// Create a test with some proxies
	proxies := []map[string]any{
		{"name": "p1", "type": "ss", "server": "1.1.1.1", "port": 8388, "cipher": "aes-128-gcm", "password": "pass"},
		{"name": "p2", "type": "ss", "server": "1.1.1.2", "port": 8388, "cipher": "aes-128-gcm", "password": "pass"},
	}
	opts := models.Options{
		Proxies:    proxies,
		Concurrent: 2,
		Timeout:    time.Second,
	}

	tester, err := NewTest(opts)
	assert.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())

	// Start test in a goroutine
	done := make(chan struct{})
	go func() {
		_, _ = tester.TestSpeed(ctx)
		close(done)
	}()

	// Immediately cancel
	cancel()

	select {
	case <-done:
		// Success: TestSpeed returned
	case <-time.After(5 * time.Second):
		t.Fatal("TestSpeed did not return after cancellation")
	}
}
