package convert

import (
	"fmt"
	"net/netip"
	"strconv"

	"github.com/xiecang/node-quality/v2/internal/clash2singbox/model/clash"
	"github.com/xiecang/node-quality/v2/internal/clash2singbox/model/singbox"
)

func wireguardEndpoint(p *clash.Proxies) (*singbox.SingBoxEndpoint, error) {
	if p.MTU < 0 || p.MTU > 65535 {
		return nil, fmt.Errorf("wireguard: invalid MTU")
	}
	address, err := addCidr([]string{p.IP, p.IPv6})
	if err != nil {
		return nil, fmt.Errorf("wireguard: %w", err)
	}

	ep := &singbox.SingBoxEndpoint{
		Type:       "wireguard",
		Tag:        p.Name,
		Address:    address,
		PrivateKey: p.PrivateKey,
		Detour:     p.DialerProxy,
		MTU:        uint32(p.MTU),
	}

	if len(p.Peers) > 0 {
		for _, peer := range p.Peers {
			if peer.Port < 1 || peer.Port > 65535 {
				return nil, fmt.Errorf("wireguard: peer port out of range")
			}
			if len(peer.AllowedIPs) == 0 {
				peer.AllowedIPs = peer.AllowedIPsLegacy
			}
			var reserved []uint8
			if peer.Reserved != nil {
				reserved = peer.Reserved.Value
			}
			ep.Peers = append(ep.Peers, &singbox.SingWireguardMultiPeer{
				Address:      peer.Server,
				Port:         uint16(peer.Port),
				PublicKey:    peer.PublicKey,
				PreSharedKey: peer.PreSharedKey,
				AllowedIps:   peer.AllowedIPs,
				Reserved:     reserved,
			})
		}
	} else {
		port, err := strconv.Atoi(p.Port)
		if err != nil || port < 1 || port > 65535 {
			return nil, fmt.Errorf("wireguard: invalid server port")
		}
		var allowed []string
		for _, value := range address {
			prefix, e := netip.ParsePrefix(value)
			if e != nil {
				return nil, e
			}
			if prefix.Addr().Is4() {
				allowed = append(allowed, "0.0.0.0/0")
			} else {
				allowed = append(allowed, "::/0")
			}
		}
		var reserved []uint8
		if p.Reserved != nil {
			reserved = p.Reserved.Value
		}
		ep.Peers = append(ep.Peers, &singbox.SingWireguardMultiPeer{
			Address:      p.Server,
			Port:         uint16(port),
			AllowedIps:   allowed,
			PublicKey:    p.PublicKey,
			PreSharedKey: p.PreSharedKey,
			Reserved:     reserved,
		})
	}

	return ep, nil
}

func addCidr(ipl []string) ([]string, error) {
	c := make([]string, 0, len(ipl))
	for _, v := range ipl {
		if v == "" {
			continue
		}
		p, err := netip.ParsePrefix(v)
		if err == nil {
			c = append(c, p.String())
			continue
		}
		ipr, err := netip.ParseAddr(v)
		if err != nil {
			return nil, fmt.Errorf("addCidr: %w", err)
		}
		if ipr.Is4() {
			c = append(c, netip.PrefixFrom(ipr, 32).String())
		}
		if ipr.Is6() {
			c = append(c, netip.PrefixFrom(ipr, 128).String())
		}
	}
	return c, nil
}
