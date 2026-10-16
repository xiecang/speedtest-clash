// SPDX-License-Identifier: GPL-3.0-only
package measure

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xiecang/node-quality/v2/internal/model"
	"golang.org/x/time/rate"
)

// Budget is shared by all evaluations in one worker, including separate jobs.
type Budget struct {
	limiter        *rate.Limiter
	bandwidth      chan struct{}
	mu             sync.Mutex
	credit         int
	bytesPerSecond int64
	downloaded     atomic.Int64
	queueWait      atomic.Int64
}

// BudgetStats is a snapshot of actual application reads across all jobs.
type BudgetStats struct {
	DownloadedBytes int64
	QueueWait       time.Duration
	ActiveNodes     int
	NodeConcurrency int
	BytesPerSecond  int64
}

func (b *Budget) Stats() BudgetStats {
	return BudgetStats{b.downloaded.Load(), time.Duration(b.queueWait.Load()), len(b.bandwidth), cap(b.bandwidth), b.bytesPerSecond}
}

func NewBudget(bytesPerSecond int64) *Budget {
	return newBudget(bytesPerSecond, 1)
}

// NewBudgetWithConcurrency shares both the rate and node slots across jobs.
func NewBudgetWithConcurrency(bytesPerSecond int64, nodes int) (*Budget, error) {
	if bytesPerSecond < 0 || nodes < 1 || nodes > 128 {
		return nil, errors.New("invalid shared bandwidth budget")
	}
	return newBudget(bytesPerSecond, nodes), nil
}

func newBudget(bytesPerSecond int64, nodes int) *Budget {
	b := &Budget{bandwidth: make(chan struct{}, nodes), bytesPerSecond: bytesPerSecond}
	if bytesPerSecond > 0 {
		b.limiter = rate.NewLimiter(rate.Limit(bytesPerSecond), 32<<10)
	}
	return b
}

// take waits BEFORE reading. Unread permits are reusable; credits expire as the
// bucket refills, so an idle worker cannot accumulate more than its burst.
func (b *Budget) take(ctx context.Context, n int) (time.Duration, error) {
	if b.limiter == nil {
		return 0, ctx.Err()
	}
	var waited time.Duration
	for {
		if err := ctx.Err(); err != nil {
			return waited, err
		}
		b.mu.Lock()
		now := time.Now()
		b.capCredit(now)
		prepaid := min(n, b.credit)
		if b.limiter.AllowN(now, n-prepaid) {
			b.credit -= prepaid
			b.mu.Unlock()
			return waited, nil
		}
		// No future reservations: cancelling one stream cannot leave another stream's
		// appointment as debt for the next node. All read permits are charged now.
		delay := time.Duration(float64(n-prepaid-int(b.limiter.TokensAt(now))) / float64(b.limiter.Limit()) * float64(time.Second))
		b.mu.Unlock()
		started := time.Now()
		timer := time.NewTimer(max(time.Microsecond, delay))
		select {
		case <-timer.C:
			waited += time.Since(started)
		case <-ctx.Done():
			timer.Stop()
			return waited + time.Since(started), ctx.Err()
		}
	}
}

func (b *Budget) capCredit(now time.Time) {
	b.credit = min(b.credit, max(0, b.limiter.Burst()-int(max(0, b.limiter.TokensAt(now)))))
}

func (b *Budget) refund(n int) {
	if b.limiter == nil || n == 0 {
		return
	}
	b.mu.Lock()
	b.credit += n
	b.capCredit(time.Now())
	b.mu.Unlock()
}

// Probe HTTP failures indicate unavailable measurement infrastructure, not a
// transport fault in the node. They never trigger node isolation.
type probeHTTPError struct{ status int }

func (e *probeHTTPError) Error() string { return fmt.Sprintf("probe HTTP status %d", e.status) }

func copyDownload(ctx context.Context, source io.Reader, quota int64, budget *Budget) (int64, time.Duration, error) {
	buffer := make([]byte, 32<<10)
	var total int64
	var wait time.Duration
	for total < quota {
		if err := ctx.Err(); err != nil {
			return total, wait, err
		}
		chunk := int64(len(buffer))
		if budget.limiter != nil {
			chunk = min(chunk, max(int64(1), budget.bytesPerSecond/20))
		}
		if quota-total < chunk {
			chunk = quota - total
		}
		elapsed, chargeErr := budget.take(ctx, int(chunk))
		wait += elapsed
		if chargeErr != nil {
			return total, wait, chargeErr
		}
		if err := ctx.Err(); err != nil {
			budget.refund(int(chunk))
			return total, wait, err
		}
		n, readErr := source.Read(buffer[:chunk])
		total += int64(n)
		budget.downloaded.Add(int64(n))
		budget.refund(int(chunk) - n)

		if readErr != nil {
			// HTTP/TLS cancellation can race the peer's clean/chunked EOF. Once
			// our context has ended, that EOF is not evidence of an early short body.
			if ctx.Err() != nil && (errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrUnexpectedEOF)) {
				return total, wait, ctx.Err()
			}
			if errors.Is(readErr, io.EOF) && total == quota {
				return total, wait, nil
			}
			if errors.Is(readErr, io.EOF) {
				readErr = io.ErrUnexpectedEOF
			}
			return total, wait, readErr
		}
		if n == 0 {
			return total, wait, io.ErrNoProgress
		}
	}
	return total, wait, nil
}

func measureDownload(ctx context.Context, client *http.Client, plan model.Plan, budget *Budget) (model.Metrics, error) {
	if budget == nil {
		return model.Metrics{}, errors.New("missing shared worker bandwidth budget")
	}
	queuedAt := time.Now()
	select {
	case budget.bandwidth <- struct{}{}:
		defer func() { <-budget.bandwidth }()
	case <-ctx.Done():
		budget.queueWait.Add(int64(time.Since(queuedAt)))
		return model.Metrics{}, ctx.Err()
	}
	started := time.Now()
	budget.queueWait.Add(int64(started.Sub(queuedAt)))
	sampleCtx, cancel := context.WithTimeout(ctx, plan.SampleWindow)
	defer cancel()
	var wg sync.WaitGroup
	var mu sync.Mutex
	metrics := model.Metrics{BandwidthQueueWait: started.Sub(queuedAt), DownloadNodeConcurrency: cap(budget.bandwidth)}
	var failures []error
	var headers []time.Duration
	for i := 0; i < plan.DownloadConcurrency; i++ {
		quota := plan.DownloadBytes / int64(plan.DownloadConcurrency)
		if int64(i) < plan.DownloadBytes%int64(plan.DownloadConcurrency) {
			quota++
		}
		if quota == 0 {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			request, err := http.NewRequestWithContext(sampleCtx, http.MethodGet, probeEndpoint(plan.ProbeURL, "download")+"?bytes="+strconv.FormatInt(quota, 10), nil)
			if err != nil {
				mu.Lock()
				failures = append(failures, err)
				mu.Unlock()
				return
			}
			setProbeHeaders(request, plan)
			wasReused := false
			request = request.WithContext(httptrace.WithClientTrace(request.Context(), &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) { wasReused = info.Reused }}))
			headerStarted := time.Now()
			response, err := client.Do(request)
			ttfb := time.Since(headerStarted)
			var count int64
			var wait time.Duration
			if err == nil {
				if response.StatusCode != http.StatusOK {
					err = &probeHTTPError{status: response.StatusCode}
				} else {
					count, wait, err = copyDownload(sampleCtx, response.Body, quota, budget)
				}
				response.Body.Close()
			}
			mu.Lock()
			if response != nil && response.StatusCode == http.StatusOK {
				headers = append(headers, ttfb)
				if wasReused {
					metrics.DownloadReusedHeaderSamples++
				}
			}

			metrics.DownloadBytes += count
			metrics.AggregateLimiterWait += wait
			// Only cancellation caused by this sample deadline is normal. A reset,
			// short body or parent cancellation must not be hidden by positive bytes.
			if err != nil && !(ctx.Err() == nil && sampleCtx.Err() == context.DeadlineExceeded && errors.Is(err, context.DeadlineExceeded)) {
				failures = append(failures, err)
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	metrics.DownloadDuration = time.Since(started)
	metrics.DownloadEndReason = "error"
	if len(failures) == 0 && ctx.Err() == nil {
		if metrics.DownloadBytes >= plan.DownloadBytes {
			metrics.DownloadEndReason = "budget"
		} else if sampleCtx.Err() == context.DeadlineExceeded {
			metrics.DownloadEndReason = "window"
		}
	}
	metrics.DownloadHeadersMedian = median(headers)
	metrics.DownloadHeaderSamples = len(headers)
	metrics.RateLimitBytesPerSecond = max(0, budget.bytesPerSecond)
	metrics.RateLimited = metrics.AggregateLimiterWait > 0
	if metrics.DownloadDuration > 0 {
		metrics.GoodputBytesPerSecond = float64(metrics.DownloadBytes) / metrics.DownloadDuration.Seconds()
	}
	if ctx.Err() != nil {
		return metrics, ctx.Err()
	}
	if len(failures) > 0 {
		return metrics, errors.Join(failures...)
	}
	if metrics.DownloadBytes == 0 {
		return metrics, errors.New("empty download sample")
	}
	return metrics, nil
}
