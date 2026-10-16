// SPDX-License-Identifier: GPL-3.0-only
package measure

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/xiecang/node-quality/v2/internal/model"
)

func streamURL(plan model.Plan, path, protocol string) string {
	return probeEndpoint(plan.ProbeURL, path) + "?duration_ms=" + strconv.FormatInt(plan.AI.Duration.Milliseconds(), 10) + "&protocol=" + url.QueryEscape(protocol)
}

func measureStreams(ctx context.Context, dial model.DialContext, plan model.Plan) []model.StreamResult {
	protocols := []string{"h1", "h2", "websocket"}
	results := make([]model.StreamResult, len(protocols))
	var wg sync.WaitGroup
	for i, protocol := range protocols {
		wg.Add(1)
		go func() { defer wg.Done(); results[i] = measureStream(ctx, dial, plan, protocol) }()
	}
	wg.Wait()
	return results
}

func measureStream(parent context.Context, dial model.DialContext, plan model.Plan, protocol string) (result model.StreamResult) {
	started := time.Now()
	result = model.StreamResult{Protocol: protocol, Status: model.Unknown, MeasuredAt: started}
	defer func() { result.Duration = time.Since(started) }()
	ctx, cancel := context.WithTimeout(parent, plan.AI.Duration+20*time.Second)
	defer cancel()
	client, transport := probeClient(dial, plan, protocol == "h2")
	defer transport.CloseIdleConnections()
	var next func(context.Context) (model.StreamFrame, error)
	var closeStream func()
	if protocol == "websocket" {
		address := streamURL(plan, "ws", protocol)
		address = strings.Replace(address, "https://", "wss://", 1)
		address = strings.Replace(address, "http://", "ws://", 1)
		headers := http.Header{}
		if plan.ProbeToken != "" {
			headers.Set("Authorization", "Bearer "+plan.ProbeToken)
		}
		connection, response, err := websocket.Dial(ctx, address, &websocket.DialOptions{HTTPClient: client, HTTPHeader: headers, CompressionMode: websocket.CompressionDisabled})
		if err != nil {
			if response != nil && response.StatusCode != http.StatusSwitchingProtocols {
				setStreamFailure(&result, nil, parent, fmt.Sprintf("probe_http_%d", response.StatusCode), true)
			} else {
				setStreamFailure(&result, err, parent, "ws_connect", false)
			}
			return
		}
		connection.SetReadLimit(4096)
		closeStream = func() { connection.CloseNow() }
		next = func(ctx context.Context) (model.StreamFrame, error) {
			_, content, err := connection.Read(ctx)
			var frame model.StreamFrame
			if err == nil {
				err = json.Unmarshal(content, &frame)
			}
			return frame, err
		}
	} else {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, streamURL(plan, "sse", protocol), nil)
		if err != nil {
			setStreamFailure(&result, err, parent, "invalid_probe_url", true)
			return
		}
		setProbeHeaders(request, plan)
		response, err := client.Do(request)
		if err != nil {
			setStreamFailure(&result, err, parent, "sse_connect", false)
			return
		}
		closeStream = func() { response.Body.Close() }
		if response.StatusCode != http.StatusOK {
			closeStream()
			setStreamFailure(&result, nil, parent, fmt.Sprintf("probe_http_%d", response.StatusCode), true)
			return
		}
		if protocol == "h2" && response.ProtoMajor != 2 {
			closeStream()
			setStreamFailure(&result, nil, parent, "h2_not_negotiated", true)
			return
		}
		if !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
			closeStream()
			setStreamFailure(&result, nil, parent, "invalid_sse_type", true)
			return
		}
		scanner := bufio.NewScanner(response.Body)
		scanner.Buffer(make([]byte, 1024), 4096)
		next = func(ctx context.Context) (model.StreamFrame, error) {
			for scanner.Scan() {
				line := scanner.Text()
				if strings.HasPrefix(line, "data: ") {
					var frame model.StreamFrame
					err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &frame)
					return frame, err
				}
			}
			err := scanner.Err()
			if err == nil {
				err = errors.New("missing end marker")
			}
			return model.StreamFrame{}, err
		}
	}
	defer closeStream()
	last := time.Now()
	idleUntil := time.Time{}
	for {
		deadline := time.Now().Add(10 * time.Second)
		if idleUntil.After(time.Now()) {
			deadline = idleUntil.Add(10 * time.Second)
		}
		readCtx, readCancel := context.WithDeadline(ctx, deadline)
		// Closing the body interrupts a blocked SSE scanner. WebSocket also reads
		// with this deadline; no helper goroutine survives the stream.
		stop := context.AfterFunc(readCtx, closeStream)
		frame, err := next(readCtx)
		stop()
		readCancel()
		if err != nil {
			baseline := last
			if !idleUntil.IsZero() {
				baseline = idleUntil
			}
			result.MaxActiveSilence = max(result.MaxActiveSilence, max(time.Duration(0), time.Since(baseline)))
			setStreamFailure(&result, err, parent, "stream_incomplete", false)
			return
		}
		now := time.Now()
		baseline := last
		if !idleUntil.IsZero() {
			baseline = idleUntil
		}
		result.MaxActiveSilence = max(result.MaxActiveSilence, max(time.Duration(0), now.Sub(baseline)))
		if result.MaxActiveSilence > 10*time.Second {
			setStreamFailure(&result, nil, parent, "active_silence", true)
			return
		}
		switch frame.Kind {
		case "idle":
			if result.IdleMarkerSeen || result.Frames == 0 || frame.Sequence != result.Frames || frame.IdleMilliseconds <= 0 || time.Duration(frame.IdleMilliseconds)*time.Millisecond > plan.AI.Duration/3+time.Second {
				setStreamFailure(&result, nil, parent, "invalid_idle_marker", true)
				return
			}
			idleUntil = now.Add(time.Duration(frame.IdleMilliseconds) * time.Millisecond)
			result.IdleMarkerSeen = true
		case "data":
			if frame.Sequence != result.Frames+1 {
				setStreamFailure(&result, nil, parent, "sequence_gap", true)
				return
			}
			gap := now.Sub(last)
			if !idleUntil.IsZero() {
				gap = now.Sub(idleUntil)
				// A marker's idle duration belongs to the server timeline. Network
				// buffering and delayed scheduling can deliver resume frames before our
				// local estimate; that is not a node fault or a broken sequence.
				gap = max(time.Duration(0), gap)
				result.ResumeObserved = true
				idleUntil = time.Time{}
			}
			if gap > result.MaxActiveSilence {
				result.MaxActiveSilence = gap
			}
			if gap > 10*time.Second {
				setStreamFailure(&result, nil, parent, "active_gap", true)
				return
			}
			result.Frames++
			last = now
		case "done":
			if frame.Sequence != result.Frames || result.Frames == 0 || !result.IdleMarkerSeen || !result.ResumeObserved || !idleUntil.IsZero() || time.Since(started) < MinimumStreamDuration(plan.AI.Duration) {
				setStreamFailure(&result, nil, parent, "invalid_end_marker", true)
				return
			}
			result.Status = model.Pass
			return
		default:
			setStreamFailure(&result, nil, parent, "invalid_frame", true)
			return
		}
	}
}

func setStreamFailure(result *model.StreamResult, err error, parent context.Context, code string, probe bool) {
	result.Status = model.Unknown
	result.FailureCode = code
	if probe {
		result.FailureOrigin = model.FailureOriginProbe
	} else {
		result.FailureOrigin, _ = failureOrigin(err, parent)
		if result.FailureOrigin == model.FailureOriginNode {
			result.FailureCode = "node_auth_required"
		} else if result.FailureOrigin == model.FailureOriginEnvironment {
			_, result.FailureCode = failureOrigin(err, parent)
		}
	}
	if result.FailureOrigin == model.FailureOriginNode {
		result.Status = model.Fail
	}
}
