// SPDX-License-Identifier: GPL-3.0-only
package engine

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"

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

func (b *Batch) openSingBox(ctx context.Context, nodes []model.Node) error {
	var usable []model.Node
	for _, n := range nodes {
		allowed := map[string]bool{"direct": true, "http": true, "socks": true, "shadowsocks": true, "vmess": true, "vless": true, "trojan": true, "hysteria": true, "hysteria2": true, "tuic": true, "anytls": true, "wireguard": true, "shadowtls": true, "ss": true, "socks5": true}
		closure := model.DependencyClosure([]model.Node{n}, nodes)
		unsafe := false
		for _, dep := range closure {
			kind, _ := dep.Config["type"].(string)
			if !allowed[kind] || kind == "wireguard" && dep.Config["system"] == true {
				unsafe = true
			}
		}
		if unsafe {
			b.failures[n.Key] = errors.New("outbound capability is disabled in a measurement runtime")
			continue
		}
		closure = model.DependencyClosure([]model.Node{n}, nodes)
		content, e := codec.EncodeNamed(closure, model.SingBoxJSON, codec.RuntimeNames(closure))
		if e == nil {
			e = CheckSingBox(ctx, content)
		}
		if e != nil {
			b.failures[n.Key] = e
		} else {
			usable = append(usable, n)
		}
	}
	nodes = usable
	if len(nodes) == 0 {
		return nil
	}
	content, err := codec.EncodeNamed(nodes, model.SingBoxJSON, codec.RuntimeNames(nodes))
	if err != nil {
		return err
	}
	ctx = include.Context(ctx)
	var options option.Options
	if err = sj.UnmarshalContext(ctx, content, &options); err != nil {
		return fmt.Errorf("parse native sing-box config: %w", err)
	}
	b.box, err = box.New(box.Options{Context: ctx, Options: options})
	if err != nil {
		return fmt.Errorf("construct native sing-box: %w", err)
	}
	if err = b.box.Start(); err != nil {
		return fmt.Errorf("start native sing-box: %w", err)
	}
	for _, n := range nodes {
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
		return err
	}
	var options option.Options
	// Native Rule values are large; avoid repeated backing-array growth during decoding.
	if count := min(len(shape.Route.Rules), 16384); count > 0 {
		options.Route = &option.RouteOptions{Rules: make([]option.Rule, 0, count)}
	}
	shape.Route.Rules = nil
	if err := sj.UnmarshalContext(ctx, content, &options); err != nil {
		return err
	}
	// Reject gVisor address failures before its constructor allocates a stack.
	for _, endpoint := range options.Endpoints {
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
	// A failed constructor must never leave the caller's endpoint manager in this slot.
	service.MustRegister[adapter.EndpointManager](ctx, nil)
	instance, err := box.New(box.Options{Context: ctx, Options: options})
	// Unstarted managers skip Close, but WireGuard already owns gVisor processors.
	if manager := service.FromContext[adapter.EndpointManager](ctx); manager != nil {
		for _, endpoint := range manager.Endpoints() {
			err = errors.Join(err, endpoint.Close())
		}
	}
	if instance != nil {
		err = errors.Join(err, instance.Close())
	}
	return err
}
