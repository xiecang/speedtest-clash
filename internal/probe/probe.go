// SPDX-License-Identifier: GPL-3.0-only
package probe

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/xiecang/node-quality/v2/internal/model"
)

type ProbeOptions struct {
	Token            string
	MaxSessions      int
	MaxDownloadBytes int64
	MaxDuration      time.Duration
	FrameInterval    time.Duration
}

// ProbeHandler is shared by the embedded and standalone servers. It never
// buffers downloads or complete streams in memory.
func ProbeHandler(options ProbeOptions) http.Handler {
	if options.MaxSessions <= 0 {
		options.MaxSessions = 128
	}
	if options.MaxDownloadBytes <= 0 {
		options.MaxDownloadBytes = 256 << 20
	}
	if options.MaxDuration <= 0 {
		options.MaxDuration = 10 * time.Minute
	}
	if options.FrameInterval <= 0 {
		options.FrameInterval = time.Second
	}
	sessions := make(chan struct{}, options.MaxSessions)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v2/probe/health", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /api/v2/probe/download", func(w http.ResponseWriter, r *http.Request) {
		n, err := strconv.ParseInt(r.URL.Query().Get("bytes"), 10, 64)
		if err != nil || n <= 0 || n > options.MaxDownloadBytes {
			http.Error(w, "invalid download byte budget", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.FormatInt(n, 10))
		w.Header().Set("Cache-Control", "no-store")
		buffer := make([]byte, 32<<10)
		for n > 0 {
			if r.Context().Err() != nil {
				return
			}
			chunk := int64(len(buffer))
			if n < chunk {
				chunk = n
			}
			written, e := w.Write(buffer[:chunk])
			if e != nil || written == 0 {
				return
			}
			n -= int64(written)
		}
	})
	stream := func(w http.ResponseWriter, r *http.Request, useWS bool) {
		ms, err := strconv.ParseInt(r.URL.Query().Get("duration_ms"), 10, 64)
		if err != nil || ms <= 0 || ms > options.MaxDuration.Milliseconds() {
			http.Error(w, "invalid stream duration", http.StatusBadRequest)
			return
		}
		duration := time.Duration(ms) * time.Millisecond
		ctx, cancel := context.WithTimeout(r.Context(), duration+15*time.Second)
		defer cancel()
		var send func(model.StreamFrame) error
		if useWS {
			connection, e := websocket.Accept(w, r, nil)
			if e != nil {
				return
			}
			defer connection.CloseNow()
			// CloseRead handles ping/pong and cancels when the peer disconnects.
			ctx = connection.CloseRead(ctx)
			send = func(frame model.StreamFrame) error {
				content, e := json.Marshal(frame)
				if e != nil {
					return e
				}
				return connection.Write(ctx, websocket.MessageText, content)
			}
		} else {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-store, no-transform")
			w.Header().Set("X-Accel-Buffering", "no")
			controller := http.NewResponseController(w)
			if e := controller.SetWriteDeadline(time.Time{}); e != nil && e != http.ErrNotSupported {
				http.Error(w, "cannot clear stream deadline", http.StatusInternalServerError)
				return
			}
			send = func(frame model.StreamFrame) error {
				content, e := json.Marshal(frame)
				if e != nil {
					return e
				}
				if _, e = fmt.Fprintf(w, "data: %s\n\n", content); e != nil {
					return e
				}
				return controller.Flush()
			}
		}
		if err := emitStream(ctx, duration, options.FrameInterval, send); err == nil && useWS {
			// The explicit done frame, rather than a graceful socket close, proves completeness.
			return
		}
	}
	mux.HandleFunc("GET /api/v2/probe/sse", func(w http.ResponseWriter, r *http.Request) { stream(w, r, false) })
	mux.HandleFunc("GET /api/v2/probe/ws", func(w http.ResponseWriter, r *http.Request) { stream(w, r, true) })
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Node-Quality-Probe", "2")
		if options.Token != "" {
			token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(token), []byte(options.Token)) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		if r.URL.Path == "/api/v2/probe/health" {
			mux.ServeHTTP(w, r)
			return
		}
		select {
		case sessions <- struct{}{}:
			defer func() { <-sessions }()
		case <-r.Context().Done():
			return
		default:
			w.Header().Set("Retry-After", "1")
			http.Error(w, "probe capacity exhausted", http.StatusServiceUnavailable)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func emitStream(ctx context.Context, duration, interval time.Duration, send func(model.StreamFrame) error) error {
	started := time.Now()
	idleStart := started.Add(duration / 3)
	idleEnd := started.Add(2 * duration / 3)
	end := started.Add(duration)
	var sequence int64
	idleSent := false
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		now := time.Now()
		if !now.Before(end) {
			return send(model.StreamFrame{Kind: "done", Sequence: sequence})
		}
		if !now.Before(idleStart) && now.Before(idleEnd) {
			if !idleSent {
				if err := send(model.StreamFrame{Kind: "idle", Sequence: sequence, IdleMilliseconds: time.Until(idleEnd).Milliseconds()}); err != nil {
					return err
				}
				idleSent = true
			}
		} else {
			sequence++
			if err := send(model.StreamFrame{Kind: "data", Sequence: sequence}); err != nil {
				return err
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
