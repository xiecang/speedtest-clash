// SPDX-License-Identifier: GPL-3.0-only
package engine

import (
	"container/list"
	"context"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync"

	ma "github.com/metacubex/mihomo/adapter"
	mc "github.com/metacubex/mihomo/constant"
	mlog "github.com/metacubex/mihomo/log"
	"github.com/xiecang/node-quality/v2/internal/codec"
	"github.com/xiecang/node-quality/v2/internal/model"
)

var silenceMihomoOnce sync.Once

func silenceMihomo() {
	silenceMihomoOnce.Do(func() {
		mlog.SetLevel(mlog.SILENT)
	})
}

func init() {
	silenceMihomo()
}

var mihomoAllowedTypes = map[string]bool{
	"ss":          true,
	"ssr":         true,
	"vmess":       true,
	"vless":       true,
	"trojan":      true,
	"hysteria":    true,
	"hysteria2":   true,
	"hy2":         true,
	"tuic":        true,
	"wireguard":   true,
	"socks5":      true,
	"http":        true,
	"snell":       true,
	"mieru":       true,
	"anytls":      true,
	"direct":      true,
	"ssh":         true,
	"sudoku":      true,
	"masque":      true,
	"trusttunnel": true,
}

func hasNonEmptyConfigValue(config map[string]any, key string) bool {
	v, ok := config[key]
	if !ok || v == nil {
		return false
	}
	switch val := v.(type) {
	case string:
		return strings.TrimSpace(val) != ""
	case int:
		return val != 0
	case int64:
		return val != 0
	case float64:
		return val != 0
	case []any:
		return len(val) > 0
	case []string:
		return len(val) > 0
	default:
		return true
	}
}

func validateMihomoConfig(config map[string]any) error {
	kind, _ := config["type"].(string)
	kind = strings.ToLower(strings.TrimSpace(kind))
	if !mihomoAllowedTypes[kind] {
		return errDisabledCapability
	}
	if hasNonEmptyConfigValue(config, "interface-name") || hasNonEmptyConfigValue(config, "routing-mark") {
		return errDisabledCapability
	}
	delete(config, "interface-name")
	delete(config, "routing-mark")
	if hasNonEmptyConfigValue(config, "ca") || hasNonEmptyConfigValue(config, "certificate") {
		return errDisabledCapability
	}
	delete(config, "ca")
	delete(config, "certificate")
	if hasNonEmptyConfigValue(config, "private-key") {
		if kind != "wireguard" && kind != "masque" && kind != "ssh" {
			return errDisabledCapability
		}
		if kind == "ssh" {
			pk, ok := config["private-key"].(string)
			if !ok || !strings.Contains(pk, "PRIVATE KEY") {
				return errDisabledCapability
			}
		}
	}
	return nil
}

func (b *Batch) openMihomo(nodes []model.Node) error {
	silenceMihomo()
	proxies := make(map[string]mc.Proxy, len(nodes))
	byKey := make(map[string]model.Node, len(nodes))
	for _, n := range nodes {
		byKey[n.Key] = n
	}
	var build func(string) (mc.Proxy, error)
	build = func(key string) (mc.Proxy, error) {
		if p, ok := proxies[key]; ok {
			return p, nil
		}
		if err, failed := b.failures[key]; failed {
			return nil, err
		}
		n := byKey[key]
		config, err := codec.ClashConfig(n)
		if err != nil {
			return nil, sanitizeEngineError(err)
		}
		if err := validateMihomoConfig(config); err != nil {
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
			return nil, sanitizeEngineError(fmt.Errorf("%s: construct Mihomo outbound: %w", key, err))
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
	if network != "tcp" && network != "tcp4" && network != "tcp6" {
		return nil, fmt.Errorf("unsupported network %s", network)
	}
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

const maxUDPCacheEntries = 4096

type udpSupportEntry struct {
	key   string
	value bool
}

type udpSupportLRU struct {
	mu    sync.Mutex
	order *list.List
	items map[string]*list.Element
}

var relayUDPCache = &udpSupportLRU{
	order: list.New(),
	items: make(map[string]*list.Element),
}

func (c *udpSupportLRU) get(key string) (bool, bool) {
	if key == "" {
		return false, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	elem, ok := c.items[key]
	if !ok {
		return false, false
	}
	c.order.MoveToFront(elem)
	return elem.Value.(udpSupportEntry).value, true
}

func (c *udpSupportLRU) put(key string, value bool) {
	if key == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if elem, ok := c.items[key]; ok {
		elem.Value = udpSupportEntry{key: key, value: value}
		c.order.MoveToFront(elem)
		return
	}
	if c.order.Len() >= maxUDPCacheEntries {
		if back := c.order.Back(); back != nil {
			c.order.Remove(back)
			delete(c.items, back.Value.(udpSupportEntry).key)
		}
	}
	c.items[key] = c.order.PushFront(udpSupportEntry{key: key, value: value})
}

func RelayCapabilities(node model.Node) model.RelayInfo {
	silenceMihomo()
	config, err := codec.ClashConfig(node)
	if err != nil {
		return model.RelayInfo{}
	}
	kind, _ := config["type"].(string)
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind == "" {
		return model.RelayInfo{}
	}
	info := model.RelayInfo{
		SupportsTCP: kind != "http",
		RequiresUDP: clashRequiresUDP(kind, config),
	}
	if cached, ok := relayUDPCache.get(node.Revision); ok {
		info.SupportsUDP = cached
		return info
	}
	var supportsUDP bool
	if validateMihomoConfig(config) == nil {
		name := node.Key
		if name == "" {
			name = "relay-check"
		}
		config["name"] = name
		delete(config, "dialer-proxy")
		if proxy, err := ma.ParseProxy(config); err == nil && proxy != nil {
			supportsUDP = proxy.SupportUDP()
			_ = proxy.Close()
		}
	}
	relayUDPCache.put(node.Revision, supportsUDP)
	info.SupportsUDP = supportsUDP
	return info
}

func clashRequiresUDP(kind string, config map[string]any) bool {
	switch kind {
	case "hysteria", "hysteria2", "hy2", "tuic", "wireguard", "shadowquic":
		return true
	case "masque":
		network, _ := config["network"].(string)
		return strings.ToLower(strings.TrimSpace(network)) != "h2"
	case "mieru":
		transport, _ := config["transport"].(string)
		if strings.EqualFold(strings.TrimSpace(transport), "udp") {
			return true
		}
	case "trusttunnel":
		if quic, _ := config["quic"].(bool); quic {
			return true
		}
	case "ss", "shadowsocks":
		plugin, _ := config["plugin"].(string)
		if strings.EqualFold(strings.TrimSpace(plugin), "kcptun") {
			return true
		}
	}
	network, _ := config["network"].(string)
	switch strings.ToLower(strings.TrimSpace(network)) {
	case "quic", "kcp":
		return true
	}
	return false
}
