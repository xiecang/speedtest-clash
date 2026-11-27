package nodequality

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/coder/websocket"
	"github.com/xiecang/node-quality/v2/engine"
)

type streamFrame struct {
	Kind             string `json:"kind"`
	Sequence         int64  `json:"sequence"`
	IdleMilliseconds int64  `json:"idle_ms,omitempty"`
}

func streamURL(plan Plan, path, protocol string) string {
	return probeEndpoint(plan.ProbeURL, path) + "?duration_ms=" + strconv.FormatInt(plan.AI.Duration.Milliseconds(), 10) + "&protocol=" + url.QueryEscape(protocol)
}

func measureStreams(ctx context.Context, dial engine.DialContext, plan Plan) []StreamResult {
	protocols := []string{"h1", "h2", "websocket"}
	results := make([]StreamResult, len(protocols))
	var wg sync.WaitGroup
	for i, protocol := range protocols {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = measureStream(ctx, dial, plan, protocol)
		}()
	}
	wg.Wait()
	return results
}

func measureStream(parent context.Context, dial engine.DialContext, plan Plan, protocol string) (result StreamResult) {
	started := time.Now()
	result = StreamResult{Protocol: protocol, Status: Unknown, MeasuredAt: started}
	stage := "connect"
	defer func() {
		result.Duration = time.Since(started)
		if result.Status != Pass {
			result.FailureStage = stage
		}
	}()
	ctx, cancel := context.WithTimeout(parent, plan.AI.Duration+20*time.Second)
	defer cancel()
	client, transport := probeClient(dial, plan, protocol == "h2")
	defer transport.CloseIdleConnections()
	var next func(context.Context) (streamFrame, error)
	var closeStream func()
	if protocol == "websocket" {
		address := streamURL(plan, "ws", protocol)
		address = strings.Replace(address, "https://", "wss://", 1)
		address = strings.Replace(address, "http://", "ws://", 1)
		headers := http.Header{}
		if plan.ProbeToken != "" {
			headers.Set("Authorization", "Bearer "+plan.ProbeToken)
		}
		connection, response, err := websocket.Dial(ctx, address, &websocket.DialOptions{
			HTTPClient:      client,
			HTTPHeader:      headers,
			CompressionMode: websocket.CompressionDisabled,
		})
		if err != nil {
			if response != nil && response.StatusCode == http.StatusProxyAuthRequired && response.Header.Get("X-Node-Quality-Probe") != "2" {
				setStreamFailure(&result, errNodeAuthRequired, parent, "node_auth_required", false)
			} else if response != nil && response.StatusCode != http.StatusSwitchingProtocols {
				setStreamFailure(&result, nil, parent, fmt.Sprintf("probe_http_%d", response.StatusCode), true)
			} else {
				setStreamFailure(&result, err, parent, "ws_connect", false)
			}
			return
		}
		if response == nil || response.Header.Get("X-Node-Quality-Probe") != "2" {
			connection.CloseNow()
			setStreamFailure(&result, errInvalidProbeSignature, parent, "invalid_probe_signature", false)
			return
		}
		connection.SetReadLimit(4096)
		closeStream = func() { connection.CloseNow() }
		next = func(ctx context.Context) (streamFrame, error) {
			_, content, err := connection.Read(ctx)
			var frame streamFrame
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
		probeSig := response.Header.Get("X-Node-Quality-Probe") == "2"
		if response.StatusCode == http.StatusProxyAuthRequired && !probeSig {
			closeStream()
			setStreamFailure(&result, errNodeAuthRequired, parent, "node_auth_required", false)
			return
		}
		if response.StatusCode != http.StatusOK {
			closeStream()
			setStreamFailure(&result, nil, parent, fmt.Sprintf("probe_http_%d", response.StatusCode), true)
			return
		}
		if !probeSig {
			closeStream()
			setStreamFailure(&result, errInvalidProbeSignature, parent, "invalid_probe_signature", false)
			return
		}
		if protocol == "h2" && response.ProtoMajor != 2 {
			closeStream()
			setStreamFailure(&result, nil, parent, "h2_not_negotiated", false)
			return
		}
		if !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
			closeStream()
			setStreamFailure(&result, nil, parent, "invalid_sse_type", false)
			return
		}
		scanner := bufio.NewScanner(response.Body)
		scanner.Buffer(make([]byte, 1024), 4096)
		next = func(ctx context.Context) (streamFrame, error) {
			for scanner.Scan() {
				line := scanner.Text()
				if strings.HasPrefix(line, "data: ") {
					var frame streamFrame
					err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &frame)
					return frame, err
				}
			}
			err := scanner.Err()
			if err == nil {
				err = io.EOF
			}
			return streamFrame{}, err
		}
	}
	defer closeStream()
	last := time.Now()
	idleUntil := time.Time{}
	for {
		stage = "active"
		if !idleUntil.IsZero() {
			stage = "idle"
			if !time.Now().Before(idleUntil) {
				stage = "resume"
			}
		} else if result.ResumeObserved {
			stage = "resumed"
		}
		deadline := time.Now().Add(10 * time.Second)
		if idleUntil.After(time.Now()) {
			deadline = idleUntil.Add(10 * time.Second)
		}
		readCtx, readCancel := context.WithDeadline(ctx, deadline)
		stop := context.AfterFunc(readCtx, closeStream)
		frame, err := next(readCtx)
		readErr := readCtx.Err()
		stop()
		readCancel()
		if err != nil {
			if stage == "idle" && !time.Now().Before(idleUntil) {
				stage = "resume"
			}
			baseline := last
			if !idleUntil.IsZero() {
				baseline = idleUntil
			}
			result.MaxActiveSilence = max(result.MaxActiveSilence, max(time.Duration(0), time.Since(baseline)))
			setStreamFailure(&result, err, parent, "stream_incomplete", false)
			if readErr != nil && parent.Err() == nil {
				result.FailureReason = streamFailureReason(readErr)
			}
			return
		}
		now := time.Now()
		baseline := last
		if !idleUntil.IsZero() {
			baseline = idleUntil
		}
		result.MaxActiveSilence = max(result.MaxActiveSilence, max(time.Duration(0), now.Sub(baseline)))
		if result.MaxActiveSilence > 10*time.Second {
			setStreamFailure(&result, nil, parent, "active_silence", false)
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
				gap = max(time.Duration(0), now.Sub(idleUntil))
				result.ResumeObserved = true
				idleUntil = time.Time{}
			}
			if gap > result.MaxActiveSilence {
				result.MaxActiveSilence = gap
			}
			if gap > 10*time.Second {
				setStreamFailure(&result, nil, parent, "active_gap", false)
				return
			}
			result.Frames++
			last = now
		case "done":
			stage = "completion"
			if frame.Sequence != result.Frames || result.Frames == 0 || !result.IdleMarkerSeen || !result.ResumeObserved || !idleUntil.IsZero() || time.Since(started) < MinimumStreamDuration(plan.AI.Duration) {
				setStreamFailure(&result, nil, parent, "invalid_end_marker", true)
				return
			}
			result.Status = Pass
			return
		default:
			setStreamFailure(&result, nil, parent, "invalid_frame", true)
			return
		}
	}
}

func setStreamFailure(result *StreamResult, err error, parent context.Context, code string, isProbe bool) {
	result.Status = Unknown
	result.FailureCode = code
	if err != nil && !errors.Is(err, errInvalidProbeSignature) {
		result.FailureReason = streamFailureReason(err)
	}
	if isProbe {
		result.FailureOrigin = FailureOriginProbe
	} else {
		origin, originCode := failureOrigin(err, parent)
		result.FailureOrigin = origin
		if origin == FailureOriginNode || origin == FailureOriginEnvironment {
			result.FailureCode = originCode
		}
	}
	if result.FailureOrigin == FailureOriginNode {
		result.Status = Fail
	}
}

func streamFailureReason(err error) string {
	var network net.Error
	var syntax *json.SyntaxError
	var value *json.UnmarshalTypeError
	switch {
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &network) && network.Timeout():
		return "timeout"
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return "eof"
	case errors.Is(err, syscall.ECONNRESET):
		return "connection_reset"
	case errors.As(err, &syntax), errors.As(err, &value):
		return "invalid_frame"
	case websocket.CloseStatus(err) != -1:
		return "websocket_close"
	default:
		return "read_error"
	}
}
