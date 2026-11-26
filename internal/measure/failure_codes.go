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

var (
	errNodeAuthRequired      = errors.New("node proxy authentication required")
	errInvalidProbeSignature = errors.New("invalid probe signature")
)

func isNodeAuthError(err error) bool {
	if err == nil {
		return false
	}
	text := err.Error()
	return strings.Contains(text, "HTTP need auth") || strings.Contains(text, "authentication required")
}

func failureOrigin(err error, parent context.Context) (model.FailureOrigin, string) {
	if errors.Is(err, socks5.ErrAuth) || errors.Is(err, errNodeAuthRequired) || isNodeAuthError(err) {
		return model.FailureOriginNode, "node_auth_required"
	}
	if parent != nil && parent.Err() != nil || errors.Is(err, context.Canceled) {
		return model.FailureOriginEnvironment, "cancelled"
	}
	if errors.Is(err, syscall.EMFILE) || errors.Is(err, syscall.ENFILE) || errors.Is(err, syscall.EADDRNOTAVAIL) || errors.Is(err, syscall.ENOBUFS) {
		return model.FailureOriginEnvironment, "environment_resource_exhausted"
	}
	if errors.Is(err, syscall.ENETUNREACH) || errors.Is(err, syscall.EHOSTUNREACH) || errors.Is(err, syscall.ENETDOWN) {
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
	case strings.Contains(text, "not included") || strings.Contains(text, "not built") || strings.Contains(text, "disabled in a measurement runtime"):
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
