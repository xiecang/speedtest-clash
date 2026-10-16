// SPDX-License-Identifier: GPL-3.0-only
package nodequality

import (
	"context"
	"net/http"

	"github.com/xiecang/node-quality/v2/internal/codec"
	"github.com/xiecang/node-quality/v2/internal/model"
	sources "github.com/xiecang/node-quality/v2/internal/source"
)

// NewNode normalizes a configuration and assigns its stable identity.
func NewNode(config map[string]any, format Format) (Node, error) {
	return codec.NewNode(config, format)
}

// Decode strictly decodes a complete source document.
func Decode(content []byte) (DecodeResult, error) { return codec.Decode(content) }

// ValidateIdentity checks that a node matches its configuration identity.
func ValidateIdentity(node Node) error { return model.ValidateIdentity(node) }

// ValidateDependencies checks dependency references and cycles.
func ValidateDependencies(nodes []Node) error { return model.ValidateDependencies(nodes) }

// Encode encodes nodes and their references in the requested format.
func Encode(nodes []Node, format Format) ([]byte, error) { return codec.Encode(nodes, format) }

// Convert strictly converts a source document and returns diagnostics.
func Convert(content []byte, format Format) ([]byte, []Diagnostic, error) {
	return codec.Convert(content, format)
}

// ConvertSelected strictly converts a document after filtering complete paths.
func ConvertSelected(ctx context.Context, content []byte, format Format, options SelectionOptions) ([]byte, []Diagnostic, error) {
	return codec.ConvertSelected(ctx, content, format, options)
}

// ClashConfig returns the Mihomo representation of a node.
func ClashConfig(node Node) (map[string]any, error) { return codec.ClashConfig(node) }

// DisplayNames assigns stable, unique display names without changing node identity.
func DisplayNames(nodes []Node, reserved ...string) map[string]string {
	return codec.DisplayNames(nodes, reserved...)
}

// ConvertRule converts one Clash rule to a native sing-box rule.
func ConvertRule(rule string) (map[string]any, error) { return codec.ConvertRule(rule) }

// RewriteRuleTarget rewrites a rule target while preserving its match conditions.
func RewriteRuleTarget(rule, target string) (string, error) {
	return codec.RewriteRuleTarget(rule, target)
}

// LoadSource loads a source and emits validated batches.
func LoadSource(ctx context.Context, source string, client *http.Client, emit func([]Node) error) error {
	return sources.LoadSource(ctx, source, client, emit)
}

// LoadSourceDetailed isolates failed providers and validates surviving dependencies before emitting.
func LoadSourceDetailed(ctx context.Context, source string, client *http.Client, emit func([]Node) error) (SourceSummary, error) {
	return sources.LoadSourceDetailed(ctx, source, client, emit)
}

// LoadSourceRequestDetailed loads one source and diagnoses isolated provider failures.
func LoadSourceRequestDetailed(ctx context.Context, request SourceRequest, client *http.Client, emit func([]Node) error) (SourceSummary, error) {
	return sources.LoadSourceRequestDetailed(ctx, request, client, emit)
}

// LoadSourcesDetailed merges ordered sources and isolates failed source/provider documents.
// File paths require SourceRequest.AllowLocalFiles, including nested providers.
func LoadSourcesDetailed(ctx context.Context, requests []SourceRequest, client *http.Client, emit func([]Node) error) (SourceSummary, error) {
	return sources.LoadSourcesDetailed(ctx, requests, client, emit)
}

// IdentityKey hashes connection settings and dependency identities, excluding presentation.
func IdentityKey(config map[string]any, format Format, dependencies []string) (string, error) {
	return model.IdentityKey(config, format, dependencies)
}
