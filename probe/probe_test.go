package probe

import (
	"bytes"
	"compress/zlib"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDownloadPayloadNonZeroAndIncompressible(t *testing.T) {
	h := Handler(Options{})
	req := httptest.NewRequest(http.MethodGet, "/api/v2/probe/download?bytes=32768", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.Len() != 32768 {
		t.Fatalf("status=%d len=%d", rec.Code, rec.Body.Len())
	}
	var compressed bytes.Buffer
	zw := zlib.NewWriter(&compressed)
	_, _ = zw.Write(rec.Body.Bytes())
	_ = zw.Close()
	if compressed.Len() < 32000 {
		t.Fatalf("download payload compressed too much: %d bytes", compressed.Len())
	}
}

func TestProbeStrictBearerPrefix(t *testing.T) {
	h := Handler(Options{Token: "secret"})
	for _, header := range []string{"", "secret", "bearer secret", "Basic secret", "Bearer wrong"} {
		req := httptest.NewRequest(http.MethodGet, "/api/v2/probe/health", nil)
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("header %q accepted with status %d", header, rec.Code)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v2/probe/health", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("valid Bearer rejected: %d", rec.Code)
	}
}

func TestEmitStreamShortDurationAndJitterGuaranteesIdleAndResume(t *testing.T) {
	var frames []streamFrame
	// Default 1s FrameInterval with short 300ms duration adapts ticker automatically.
	if err := emitStream(context.Background(), 300*time.Millisecond, time.Second, func(f streamFrame) error {
		frames = append(frames, f)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	idleSeen := false
	for _, f := range frames {
		if f.Kind == "idle" {
			idleSeen = true
		}
	}
	if !idleSeen || len(frames) < 4 || frames[len(frames)-1].Kind != "done" || frames[len(frames)-2].Kind != "data" {
		t.Fatalf("expected data, idle, post-idle data, done for short duration; got %+v", frames)
	}

	frames = nil
	if err := emitStream(context.Background(), 60*time.Millisecond, 15*time.Millisecond, func(f streamFrame) error {
		frames = append(frames, f)
		if f.Kind == "idle" {
			time.Sleep(50 * time.Millisecond)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(frames) < 4 || frames[len(frames)-1].Kind != "done" || frames[len(frames)-2].Kind != "data" {
		t.Fatalf("missing post-idle data frame before done: %+v", frames)
	}
}

func TestStreamIdleBoundsCapsLongSilentGapBelowProxyTimeout(t *testing.T) {
	t0 := time.Unix(1700000000, 0)
	startShort, endShort := streamIdleBounds(t0, 600*time.Millisecond)
	if endShort.Sub(startShort) != 200*time.Millisecond || startShort.Sub(t0) != 200*time.Millisecond {
		t.Fatalf("unexpected short bounds: start=%v end=%v", startShort.Sub(t0), endShort.Sub(t0))
	}
	for _, total := range []time.Duration{90 * time.Second, 180 * time.Second} {
		idleStart, idleEnd := streamIdleBounds(t0, total)
		gap := idleEnd.Sub(idleStart)
		if gap != 25*time.Second {
			t.Fatalf("total=%s expected 25s capped idle gap, got %s", total, gap)
		}
		if idleStart.Sub(t0) < 30*time.Second || t0.Add(total).Sub(idleEnd) < 30*time.Second {
			t.Fatalf("total=%s expected active phases around idle window, got start=%s end=%s", total, idleStart.Sub(t0), idleEnd.Sub(t0))
		}
	}
}
