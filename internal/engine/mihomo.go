// SPDX-License-Identifier: GPL-3.0-only
package engine

import (
	"context"
	"fmt"
	"net"
	"net/netip"

	ma "github.com/metacubex/mihomo/adapter"
	mc "github.com/metacubex/mihomo/constant"
	"github.com/xiecang/node-quality/v2/internal/codec"
	"github.com/xiecang/node-quality/v2/internal/model"
)

func (b *Batch) openMihomo(nodes []model.Node) error {
	proxies := make(map[string]mc.Proxy)
	byKey := make(map[string]model.Node)
	for _, n := range nodes {
		byKey[n.Key] = n
	}
	var build func(string) (mc.Proxy, error)
	build = func(key string) (mc.Proxy, error) {
		if p, ok := proxies[key]; ok {
			return p, nil
		}
		n := byKey[key]
		config, err := codec.ClashConfig(n)
		if err != nil {
			return nil, err
		}
		config["name"] = n.Key
		var options []ma.ProxyOption
		if len(n.Dependencies) > 1 {
			return nil, fmt.Errorf("%s: only one dialer dependency is supported", key)
		}
		if len(n.Dependencies) == 1 {
			dependency, err := build(n.Dependencies[0])
			if err != nil {
				return nil, err
			}
			delete(config, "dialer-proxy")
			options = append(options, ma.WithDialerForAPI(mihomoDialer{dependency}))
		}
		p, err := ma.ParseProxy(config, options...)
		if err != nil {
			return nil, fmt.Errorf("%s: construct Mihomo outbound: %w", key, err)
		}
		proxies[key] = p
		b.proxies = append(b.proxies, p)
		b.dialers[key] = func(ctx context.Context, network, address string) (net.Conn, error) {
			if network != "tcp" && network != "tcp4" && network != "tcp6" {
				return nil, fmt.Errorf("unsupported network %s", network)
			}
			metadata := &mc.Metadata{NetWork: mc.TCP}
			if err := metadata.SetRemoteAddress(address); err != nil {
				return nil, err
			}
			return p.DialContext(ctx, metadata)
		}
		return p, nil
	}
	for _, n := range nodes {
		if _, err := build(n.Key); err != nil {
			b.failures[n.Key] = err
		}
	}
	return nil
}

type mihomoDialer struct{ proxy mc.Proxy }

func (d mihomoDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	metadata := &mc.Metadata{NetWork: mc.TCP}
	if err := metadata.SetRemoteAddress(address); err != nil {
		return nil, err
	}
	return d.proxy.DialContext(ctx, metadata)
}

func (d mihomoDialer) ListenPacket(ctx context.Context, network, address string, remote netip.AddrPort) (net.PacketConn, error) {
	metadata := &mc.Metadata{NetWork: mc.UDP}
	if err := metadata.SetRemoteAddress(remote.String()); err != nil {
		return nil, err
	}
	return d.proxy.ListenPacketContext(ctx, metadata)
}
