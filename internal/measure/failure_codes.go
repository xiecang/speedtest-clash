// SPDX-License-Identifier: GPL-3.0-only
package measure

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"strings"
	"syscall"

	"github.com/metacubex/mihomo/transport/socks5"
	"github.com/xiecang/node-quality/v2/internal/model"
)

func failureOrigin(err error, parent context.Context) (model.FailureOrigin, string) {
	if errors.Is(err, socks5.ErrAuth) {
		return model.FailureOriginNode, "node_auth_required"
	}
	if parent != nil && parent.Err() != nil || errors.Is(err, context.Canceled) {
		return model.FailureOriginEnvironment, "cancelled"
	}
	if errors.Is(err, syscall.EMFILE) || errors.Is(err, syscall.ENFILE) {
		return model.FailureOriginEnvironment, "environment_resource_exhausted"
	}
	if errors.Is(err, syscall.ENETUNREACH) {
		return model.FailureOriginEnvironment, "environment_route_unreachable"
	}
	return model.FailureOriginUnknown, ""
}

// Export categories, never engine error strings that may contain node credentials.
func capabilityFailureCode(err error) string {
	text := err.Error()
	switch {
	case strings.Contains(text, "packet encoding"):
		return "packet_encoding_conflict"
	case strings.Contains(text, "cannot be represented") || strings.Contains(text, "unmapped") || strings.Contains(text, "lossless"):
		return "unsupported_configuration"
	case strings.Contains(text, "not included") || strings.Contains(text, "not built"):
		return "unsupported_core"
	default:
		return "core_initialization_failed"
	}
}

func networkFailureCode(err error, fallback string) string {
	var certificate *tls.CertificateVerificationError
	if errors.As(err, &certificate) {
		return "certificate_failure"
	}
	var network net.Error
	if errors.As(err, &network) && network.Timeout() {
		return "network_timeout"
	}
	return fallback
}
