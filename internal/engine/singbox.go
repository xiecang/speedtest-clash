// SPDX-License-Identifier: GPL-3.0-only
package engine

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"regexp"
	"strings"

	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-tun"
	sj "github.com/sagernet/sing/common/json"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/sagernet/sing/service"
	"github.com/xiecang/node-quality/v2/internal/codec"
	"github.com/xiecang/node-quality/v2/internal/model"
)

var errDisabledCapability = errors.New("outbound capability is disabled in a measurement runtime")

var singBoxAllowedTypes = map[string]bool{
	"direct":      true,
	"http":        true,
	"socks":       true,
	"shadowsocks": true,
	"vmess":       true,
	"vless":       true,
	"trojan":      true,
	"hysteria":    true,
	"hysteria2":   true,
	"tuic":        true,
	"anytls":      true,
	"wireguard":   true,
	"shadowtls":   true,
	"ss":          true,
	"socks5":      true,
}

var sensitiveKeyPattern = regexp.MustCompile(`(?i)("?(?:private[_-]key|preshared[_-]key)"?\s*[=:]\s*)("[^"]*"|'[^']*'|[^\s,;)}\]]+)`)

func sanitizeEngineError(err error) error {
	if err == nil {
		return nil
	}
	original := err.Error()
	sanitized := sanitizeEngineErrorText(original)
	if sanitized == original {
		return err
	}
	return errors.New(sanitized)
}

func sanitizeEngineErrorText(text string) string {
	sanitized := sensitiveKeyPattern.ReplaceAllString(text, "${1}[REDACTED]")
	lower := strings.ToLower(sanitized)
	const marker = "setup wireguard:"
	if idx := strings.Index(lower, marker); idx != -1 {
		rest := sanitized[idx+len(marker):]
		if strings.Contains(rest, "\n") {
			lastNL := strings.LastIndex(rest, "\n")
			tail := strings.TrimSpace(rest[lastNL+1:])
			if strings.HasPrefix(tail, ":") && !strings.Contains(tail, "=") {
				sanitized = sanitized[:idx] + "setup wireguard: [REDACTED]" + tail
			} else {
				sanitized = sanitized[:idx] + "setup wireguard: [REDACTED]"
			}
		}
	}
	return sanitized
}

func validateWireGuardEndpoints(endpoints []option.Endpoint) error {
	// Reject gVisor address failures before its constructor allocates a stack.
	for _, endpoint := range endpoints {
		if wg, ok := endpoint.Options.(*option.WireGuardEndpointOptions); ok && tun.WithGVisor {
			// gVisor registers the IPv4 broadcast address before user addresses.
			seen := map[netip.Addr]bool{netip.AddrFrom4([4]byte{255, 255, 255, 255}): true}
			for _, prefix := range wg.Address {
				if !prefix.IsValid() {
					return errors.New("invalid WireGuard local address")
				}
				if seen[prefix.Addr()] {
					return errors.New("duplicate WireGuard local address")
				}
				seen[prefix.Addr()] = true
			}
		}
	}
	return nil
}

func closeSingBox(instance *box.Box, endpoints []adapter.Endpoint) error {
	var err error
	for _, endpoint := range endpoints {
		if e := endpoint.Close(); e != nil && !errors.Is(e, os.ErrClosed) && !errors.Is(e, net.ErrClosed) {
			err = errors.Join(err, e)
		}
	}
	if instance != nil {
		if e := instance.Close(); e != nil && !errors.Is(e, os.ErrClosed) && !errors.Is(e, net.ErrClosed) {
			err = errors.Join(err, e)
		}
	}
	return err
}

func startSingBoxSubset(ctx context.Context, nodes []model.Node) (*box.Box, []adapter.Endpoint, error) {
	content, err := codec.EncodeNamed(nodes, model.SingBoxJSON, codec.RuntimeNames(nodes))
	if err != nil {
		return nil, nil, sanitizeEngineError(err)
	}
	boxCtx := include.Context(service.ExtendContext(ctx))
	var options option.Options
	if err = sj.UnmarshalContext(boxCtx, content, &options); err != nil {
		return nil, nil, sanitizeEngineError(fmt.Errorf("parse native sing-box config: %w", err))
	}
	if err = validateWireGuardEndpoints(options.Endpoints); err != nil {
		return nil, nil, sanitizeEngineError(err)
	}
	service.MustRegister[adapter.EndpointManager](boxCtx, nil)
	instance, err := box.New(box.Options{Context: boxCtx, Options: options})
	var endpoints []adapter.Endpoint
	if manager := service.FromContext[adapter.EndpointManager](boxCtx); manager != nil {
		endpoints = manager.Endpoints()
	}
	if err != nil {
		_ = closeSingBox(instance, endpoints)
		return nil, nil, sanitizeEngineError(fmt.Errorf("construct native sing-box: %w", err))
	}
	if err = instance.Start(); err != nil {
		_ = closeSingBox(instance, endpoints)
		return nil, nil, sanitizeEngineError(fmt.Errorf("start native sing-box: %w", err))
	}
	return instance, endpoints, nil
}

func (b *Batch) openSingBox(ctx context.Context, nodes []model.Node) error {
	if len(nodes) == 0 {
		return nil
	}
	byKey := make(map[string]model.Node, len(nodes))
	for _, n := range nodes {
		byKey[n.Key] = n
	}
	unsafeMemo := make(map[string]bool, len(nodes))
	var isUnsafe func(string) bool
	isUnsafe = func(key string) bool {
		if v, ok := unsafeMemo[key]; ok {
			return v
		}
		n, ok := byKey[key]
		if !ok {
			unsafeMemo[key] = true
			return true
		}
		kind, _ := n.Config["type"].(string)
		if !singBoxAllowedTypes[kind] || kind == "wireguard" && n.Config["system"] == true {
			unsafeMemo[key] = true
			return true
		}
		for _, dep := range n.Dependencies {
			if isUnsafe(dep) {
				unsafeMemo[key] = true
				return true
			}
		}
		unsafeMemo[key] = false
		return false
	}

	usable := make([]model.Node, 0, len(nodes))
	seenUsable := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		if isUnsafe(n.Key) {
			b.failures[n.Key] = errDisabledCapability
			continue
		}
		if !seenUsable[n.Key] {
			seenUsable[n.Key] = true
			usable = append(usable, n)
		}
	}
	if len(usable) == 0 {
		return nil
	}

	instance, endpoints, err := startSingBoxSubset(ctx, usable)
	if err != nil {
		if len(usable) == 1 {
			b.failures[usable[0].Key] = err
			return nil
		}
		if ctx.Err() != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
			return err
		}

		closureNodes := func(subset []model.Node) []model.Node {
			needed := make(map[string]bool, len(subset))
			var mark func(string)
			mark = func(key string) {
				if needed[key] {
					return
				}
				needed[key] = true
				for _, dep := range byKey[key].Dependencies {
					mark(dep)
				}
			}
			for _, n := range subset {
				mark(n.Key)
			}
			out := make([]model.Node, 0, len(needed))
			for _, n := range usable {
				if needed[n.Key] {
					out = append(out, n)
				}
			}
			return out
		}

		var isolate func(candidates []model.Node, rootFailed bool)
		isolate = func(candidates []model.Node, rootFailed bool) {
			if len(candidates) == 0 {
				return
			}
			if !rootFailed {
				closure := closureNodes(candidates)
				inst, eps, subErr := startSingBoxSubset(ctx, closure)
				_ = closeSingBox(inst, eps)
				if subErr == nil {
					return
				}
				if len(candidates) == 1 {
					b.failures[candidates[0].Key] = subErr
					return
				}
			} else if len(candidates) == 1 {
				closure := closureNodes(candidates)
				inst, eps, subErr := startSingBoxSubset(ctx, closure)
				_ = closeSingBox(inst, eps)
				if subErr != nil {
					b.failures[candidates[0].Key] = subErr
				}
				return
			}
			mid := len(candidates) / 2
			isolate(candidates[:mid], false)
			isolate(candidates[mid:], false)
		}
		isolate(usable, true)

		var closureErr func(string) error
		closureErr = func(key string) error {
			if e := b.failures[key]; e != nil {
				return e
			}
			for _, dep := range byKey[key].Dependencies {
				if e := closureErr(dep); e != nil {
					b.failures[key] = e
					return e
				}
			}
			return nil
		}

		healthy := make([]model.Node, 0, len(usable))
		for _, n := range usable {
			if closureErr(n.Key) == nil {
				healthy = append(healthy, n)
			}
		}
		if len(healthy) == 0 {
			return nil
		}
		instance, endpoints, err = startSingBoxSubset(ctx, healthy)
		if err != nil {
			return err
		}
		usable = healthy
	}

	b.box = instance
	b.endpoints = endpoints
	for _, n := range usable {
		outbound, ok := b.box.Outbound().Outbound(n.Key)
		if !ok {
			return fmt.Errorf("missing native sing-box outbound %s", n.Key)
		}
		b.dialers[n.Key] = func(ctx context.Context, network, address string) (net.Conn, error) {
			return outbound.DialContext(ctx, network, M.ParseSocksaddr(address))
		}
	}
	return nil
}

// CheckSingBox constructs but does not start the native runtime.
func CheckSingBox(ctx context.Context, content []byte) error {
	ctx = include.Context(service.ExtendContext(ctx))
	var shape struct {
		Route struct {
			Rules []struct{}
		}
	}
	if err := sj.UnmarshalContext(ctx, content, &shape); err != nil {
		return sanitizeEngineError(err)
	}
	var options option.Options
	// Native Rule values are large; avoid repeated backing-array growth during decoding.
	if count := min(len(shape.Route.Rules), 16384); count > 0 {
		options.Route = &option.RouteOptions{Rules: make([]option.Rule, 0, count)}
	}
	shape.Route.Rules = nil
	if err := sj.UnmarshalContext(ctx, content, &options); err != nil {
		return sanitizeEngineError(err)
	}
	if err := validateWireGuardEndpoints(options.Endpoints); err != nil {
		return sanitizeEngineError(err)
	}
	// A failed constructor must never leave the caller's endpoint manager in this slot.
	service.MustRegister[adapter.EndpointManager](ctx, nil)
	instance, err := box.New(box.Options{Context: ctx, Options: options})
	// Unstarted managers skip Close, but WireGuard already owns gVisor processors.
	var endpoints []adapter.Endpoint
	if manager := service.FromContext[adapter.EndpointManager](ctx); manager != nil {
		endpoints = manager.Endpoints()
	}
	err = errors.Join(sanitizeEngineError(err), closeSingBox(instance, endpoints))
	return err
}
