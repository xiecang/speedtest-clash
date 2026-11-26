// SPDX-License-Identifier: GPL-3.0-only
package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/xiecang/node-quality/v2/internal/measure"
	"github.com/xiecang/node-quality/v2/internal/model"
	"github.com/xiecang/node-quality/v2/internal/probe"
)

func TestHTTPAPIStrictBearerPrefix(t *testing.T) {
	h := Handler(model.DefaultPlan(), measure.NewBudget(0), probe.ProbeOptions{Token: "secret"})
	for _, header := range []string{"", "secret", "bearer secret", "Bearer wrong"} {
		req := httptest.NewRequest(http.MethodGet, "/api/v2/capabilities", nil)
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("header %q accepted with status %d", header, rec.Code)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v2/capabilities", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid Bearer rejected: %d", rec.Code)
	}
}
