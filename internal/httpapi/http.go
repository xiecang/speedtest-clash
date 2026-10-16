// SPDX-License-Identifier: GPL-3.0-only
package httpapi

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/xiecang/node-quality/v2/internal/codec"
	"github.com/xiecang/node-quality/v2/internal/engine"
	"github.com/xiecang/node-quality/v2/internal/filter"
	"github.com/xiecang/node-quality/v2/internal/measure"
	"github.com/xiecang/node-quality/v2/internal/model"
	probeapi "github.com/xiecang/node-quality/v2/internal/probe"
)

// Handler exposes only V2 endpoints and shares the supplied worker-wide budget.
func Handler(plan model.Plan, budget *measure.Budget, probe probeapi.ProbeOptions) http.Handler {
	slots := make(chan struct{}, 2)
	conversions := make(chan struct{}, 1)
	mux := http.NewServeMux()
	mux.Handle("/api/v2/probe/", probeapi.ProbeHandler(probe))
	protect := func(handler http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if probe.Token != "" && subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")), []byte(probe.Token)) != 1 {
				http.Error(w, "unauthorized", 401)
				return
			}
			handler(w, r)
		}
	}
	mux.HandleFunc("GET /api/v2/capabilities", protect(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(engine.Capabilities())
	}))
	mux.HandleFunc("POST /api/v2/convert", protect(func(w http.ResponseWriter, r *http.Request) {
		select {
		case conversions <- struct{}{}:
			defer func() { <-conversions }()
		default:
			http.Error(w, "conversion capacity exhausted", 429)
			return
		}
		content, err := io.ReadAll(http.MaxBytesReader(w, r.Body, codec.MaxSourceBytes))
		if err != nil {
			http.Error(w, "invalid or oversized source", 400)
			return
		}
		query := r.URL.Query()
		output, diagnostics, err := codec.ConvertSelected(r.Context(), content, model.Format(query.Get("format")), filter.Options{Filter: query.Get("filter"), NameRegexContain: query.Get("name_regex_contain"), NameRegexNotContain: query.Get("name_regex_not_contain")})
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(422)
			json.NewEncoder(w).Encode(map[string]any{"schema_version": 2, "error": err.Error(), "diagnostics": diagnostics})
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(output)
	}))
	mux.HandleFunc("POST /api/v2/evaluate", protect(func(w http.ResponseWriter, r *http.Request) {
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			http.Error(w, "evaluation capacity exhausted", 429)
			return
		}
		var input struct {
			SchemaVersion int          `json:"schema_version"`
			Nodes         []model.Node `json:"nodes"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, codec.MaxSourceBytes))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil || input.SchemaVersion != 2 {
			http.Error(w, "invalid V2 evaluation request", 400)
			return
		}
		if decoder.Decode(new(any)) != io.EOF {
			http.Error(w, "JSON must contain one object", 400)
			return
		}
		reports, err := measure.Evaluate(r.Context(), input.Nodes, plan, budget)
		if err != nil {
			http.Error(w, err.Error(), 422)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"schema_version": 2, "reports": reports})
	}))
	return mux
}
