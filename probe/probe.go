package probe

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
)

var downloadPayload [32 << 10]byte

func init() {
	var seed [32]byte
	copy(seed[:], "node-quality-v2-download-payload")
	rng := rand.NewChaCha8(seed)
	_, _ = rng.Read(downloadPayload[:])
}

type streamFrame struct {
	Kind             string `json:"kind"`
	Sequence         int64  `json:"sequence"`
	IdleMilliseconds int64  `json:"idle_ms,omitempty"`
}

type Options struct {
	Token            string
	MaxSessions      int
	MaxDownloadBytes int64
	MaxDuration      time.Duration
	FrameInterval    time.Duration
}

// Handler serves /api/v2/probe/{health,download,sse,ws} without buffering payloads in memory.
func Handler(options Options) http.Handler {
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
	mux.HandleFunc("GET /api/v2/probe/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /api/v2/probe/download", func(w http.ResponseWriter, r *http.Request) {
		n, err := strconv.ParseInt(r.URL.Query().Get("bytes"), 10, 64)
		if err != nil || n <= 0 || n > options.MaxDownloadBytes {
			http.Error(w, "invalid download byte budget", http.StatusBadRequest)
			return
		}
		controller := http.NewResponseController(w)
		if e := controller.SetWriteDeadline(time.Time{}); e != nil && !errors.Is(e, http.ErrNotSupported) {
			http.Error(w, "cannot clear download deadline", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.FormatInt(n, 10))
		w.Header().Set("Cache-Control", "no-store")
		for n > 0 {
			if r.Context().Err() != nil {
				return
			}
			chunk := int64(len(downloadPayload))
			if n < chunk {
				chunk = n
			}
			written, e := w.Write(downloadPayload[:chunk])
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
		var send func(streamFrame) error
		if useWS {
			connection, e := websocket.Accept(w, r, nil)
			if e != nil {
				return
			}
			defer connection.CloseNow()
			ctx = connection.CloseRead(ctx)
			send = func(frame streamFrame) error {
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
			if e := controller.SetWriteDeadline(time.Time{}); e != nil && !errors.Is(e, http.ErrNotSupported) {
				http.Error(w, "cannot clear stream deadline", http.StatusInternalServerError)
				return
			}
			send = func(frame streamFrame) error {
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
		_ = emitStream(ctx, duration, options.FrameInterval, send)
	}
	mux.HandleFunc("GET /api/v2/probe/sse", func(w http.ResponseWriter, r *http.Request) { stream(w, r, false) })
	mux.HandleFunc("GET /api/v2/probe/ws", func(w http.ResponseWriter, r *http.Request) { stream(w, r, true) })
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Node-Quality-Probe", "2")
		if options.Token != "" {
			token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !ok || subtle.ConstantTimeCompare([]byte(token), []byte(options.Token)) != 1 {
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

const maxSilentIdleWindow = 25 * time.Second

func streamIdleBounds(started time.Time, duration time.Duration) (time.Time, time.Time) {
	idleDuration := min(duration/3, maxSilentIdleWindow)
	idleStart := started.Add((duration - idleDuration) / 2)
	return idleStart, idleStart.Add(idleDuration)
}

func emitStream(ctx context.Context, duration, interval time.Duration, send func(streamFrame) error) error {
	if duration < 3*interval {
		interval = max(10*time.Millisecond, duration/6)
	}
	started := time.Now()
	idleStart, idleEnd := streamIdleBounds(started, duration)
	end := started.Add(duration)
	var sequence int64
	idleSent := false
	resumedSent := false
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		now := time.Now()
		if !now.Before(end) {
			if !idleSent {
				if err := send(streamFrame{Kind: "idle", Sequence: sequence, IdleMilliseconds: 1}); err != nil {
					return err
				}
				idleSent = true
			}
			if !resumedSent {
				sequence++
				if err := send(streamFrame{Kind: "data", Sequence: sequence}); err != nil {
					return err
				}
				resumedSent = true
			}
			return send(streamFrame{Kind: "done", Sequence: sequence})
		}
		if !now.Before(idleStart) && now.Before(idleEnd) {
			if !idleSent {
				idleMS := max(int64(1), idleEnd.Sub(now).Milliseconds())
				if err := send(streamFrame{Kind: "idle", Sequence: sequence, IdleMilliseconds: idleMS}); err != nil {
					return err
				}
				idleSent = true
			}
		} else {
			sequence++
			if err := send(streamFrame{Kind: "data", Sequence: sequence}); err != nil {
				return err
			}
			if idleSent {
				resumedSent = true
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
