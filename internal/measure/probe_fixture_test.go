// SPDX-License-Identifier: GPL-3.0-only
package measure

import (
	"bytes"
	"net/http"
	"time"
)

type burstWriter struct {
	http.ResponseWriter
	buffer bytes.Buffer
	last   time.Time
}

func (w *burstWriter) Write(p []byte) (int, error) { return w.buffer.Write(p) }
func (w *burstWriter) Flush() {
	if time.Since(w.last) >= 40*time.Millisecond {
		w.flush()
	}
}
func (w *burstWriter) flush() {
	_, _ = w.buffer.WriteTo(w.ResponseWriter)
	w.last = time.Now()
	w.ResponseWriter.(http.Flusher).Flush()
}

func (w *burstWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
