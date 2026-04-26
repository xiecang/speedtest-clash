package models

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestBandwidthLimiterHonorsContext(t *testing.T) {
	lim := NewBandwidthLimiter(1) // 1 byte/sec

	if err := lim.Wait(context.Background(), 1); err != nil {
		t.Fatalf("first Wait returned error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	err := lim.Wait(ctx, 1)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait error = %v, want %v", err, context.DeadlineExceeded)
	}
}

func TestBandwidthLimiterNilIsNoop(t *testing.T) {
	var lim *BandwidthLimiter // nil
	if err := lim.Wait(context.Background(), 1024); err != nil {
		t.Fatalf("nil BandwidthLimiter.Wait should be a no-op, got: %v", err)
	}
}

func TestNewBandwidthLimiterZeroIsNil(t *testing.T) {
	if NewBandwidthLimiter(0) != nil {
		t.Fatal("expected nil for maxBytesPerSec=0")
	}
	if NewBandwidthLimiter(-1) != nil {
		t.Fatal("expected nil for maxBytesPerSec=-1")
	}
}
