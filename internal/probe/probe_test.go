// SPDX-License-Identifier: GPL-3.0-only
package probe

import (
	"bytes"
	"compress/zlib"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/xiecang/node-quality/v2/internal/model"
)

func TestDownloadPayloadNonZeroAndIncompressible(t *testing.T) {
	h := ProbeHandler(ProbeOptions{})
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
	h := ProbeHandler(ProbeOptions{Token: "secret"})
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

func TestEmitStreamGuaranteesResumeFrameWhenJitterJumpsToEnd(t *testing.T) {
	var frames []model.StreamFrame
	// Interval (25ms) and duration (60ms): after idle at ~20ms, sleeping during send(idle)
	// simulates timer jitter jumping straight past end (60ms) before the next loop iteration.
	err := emitStream(context.Background(), 60*time.Millisecond, 15*time.Millisecond, func(f model.StreamFrame) error {
		frames = append(frames, f)
		if f.Kind == "idle" {
			time.Sleep(50 * time.Millisecond)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) < 4 {
		t.Fatalf("expected data, idle, post-idle data, done; got %+v", frames)
	}
	idleIdx := -1
	for i, f := range frames {
		if f.Kind == "idle" {
			idleIdx = i
			break
		}
	}
	if idleIdx == -1 || frames[len(frames)-1].Kind != "done" || frames[len(frames)-2].Kind != "data" || idleIdx >= len(frames)-2 {
		t.Fatalf("missing post-idle data frame before done: %+v", frames)
	}
}
