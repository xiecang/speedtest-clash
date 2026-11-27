package nodequality

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/xiecang/node-quality/v2/codec"
	"github.com/xiecang/node-quality/v2/engine"
	"github.com/xiecang/node-quality/v2/filter"
	"github.com/xiecang/node-quality/v2/probe"
)

// ServerHandler mounts /api/v2/probe/* along with authenticated /api/v2/{capabilities,convert,evaluate}.
func ServerHandler(plan Plan, budget *Budget, probeOpts probe.Options) http.Handler {
	slots := make(chan struct{}, 2)
	conversions := make(chan struct{}, 1)
	mux := http.NewServeMux()
	mux.Handle("/api/v2/probe/", probe.Handler(probeOpts))
	protect := func(handler http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if probeOpts.Token != "" {
				token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
				if !ok || subtle.ConstantTimeCompare([]byte(token), []byte(probeOpts.Token)) != 1 {
					http.Error(w, "unauthorized", http.StatusUnauthorized)
					return
				}
			}
			handler(w, r)
		}
	}
	mux.HandleFunc("GET /api/v2/capabilities", protect(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(engine.Capabilities())
	}))
	mux.HandleFunc("POST /api/v2/convert", protect(func(w http.ResponseWriter, r *http.Request) {
		select {
		case conversions <- struct{}{}:
			defer func() { <-conversions }()
		default:
			http.Error(w, "conversion capacity exhausted", http.StatusTooManyRequests)
			return
		}
		content, err := io.ReadAll(http.MaxBytesReader(w, r.Body, codec.MaxSourceBytes))
		if err != nil {
			http.Error(w, "invalid or oversized source", http.StatusBadRequest)
			return
		}
		query := r.URL.Query()
		output, diagnostics, err := convertInlineSource(content, codec.Format(query.Get("format")), filter.Options{
			Filter:              query.Get("filter"),
			NameRegexContain:    query.Get("name_regex_contain"),
			NameRegexNotContain: query.Get("name_regex_not_contain"),
		})
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnprocessableEntity)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"schema_version": codec.SchemaVersion,
				"error":          err.Error(),
				"diagnostics":    diagnostics,
			})
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(output)
	}))
	mux.HandleFunc("POST /api/v2/evaluate", protect(func(w http.ResponseWriter, r *http.Request) {
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			http.Error(w, "evaluation capacity exhausted", http.StatusTooManyRequests)
			return
		}
		var input struct {
			SchemaVersion int          `json:"schema_version"`
			Nodes         []codec.Node `json:"nodes"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, codec.MaxSourceBytes))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil || input.SchemaVersion != codec.SchemaVersion {
			http.Error(w, "invalid V2 evaluation request", http.StatusBadRequest)
			return
		}
		if decoder.Decode(new(any)) != io.EOF {
			http.Error(w, "JSON must contain one object", http.StatusBadRequest)
			return
		}
		reports, err := Evaluate(r.Context(), input.Nodes, plan, budget)
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"schema_version": codec.SchemaVersion,
			"reports":        reports,
		})
	}))
	return mux
}

func convertInlineSource(content []byte, format codec.Format, opts filter.Options) ([]byte, []codec.Diagnostic, error) {
	decoded, err := codec.Decode(content)
	if err != nil {
		return nil, decoded.Diagnostics, err
	}
	if len(decoded.Diagnostics) > 0 {
		return nil, decoded.Diagnostics, errors.New("source contains unsupported entries")
	}
	if len(decoded.Providers) > 0 && len(decoded.Nodes) == 0 {
		return nil, decoded.Diagnostics, errors.New("external proxy-providers require codec.LoadSource; inline conversion only converts inline nodes")
	}
	selector, err := filter.Compile(opts)
	if err != nil {
		return nil, decoded.Diagnostics, err
	}
	nodes, err := selector.Prune(decoded.Nodes)
	if err != nil {
		return nil, decoded.Diagnostics, err
	}
	output, err := codec.Encode(nodes, format)
	return output, decoded.Diagnostics, err
}
