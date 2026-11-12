// SPDX-License-Identifier: GPL-3.0-only
package codec

import (
	"fmt"

	"github.com/metacubex/mihomo/common/structure"
	_ "github.com/metacubex/mihomo/config" // Registers the DNS parser required by native WireGuard construction.
	"github.com/metacubex/mihomo/dns"
)

func validateWireGuardDNS(config map[string]any) error {
	if config["type"] != "wireguard" {
		return nil
	}
	var option struct {
		RemoteDNS bool     `proxy:"remote-dns-resolve,omitempty"`
		DNS       []string `proxy:"dns,omitempty"`
	}
	decoder := structure.NewDecoder(structure.Option{TagName: "proxy", WeaklyTypedInput: true, KeyReplacer: structure.DefaultKeyReplacer})
	if err := decoder.Decode(config, &option); err != nil {
		return err
	}
	if !option.RemoteDNS || len(option.DNS) == 0 {
		return nil
	}
	// The native constructor allocates its device before parsing DNS and leaks it on parse failure.
	if _, err := dns.ParseNameServer(option.DNS); err != nil {
		return fmt.Errorf("invalid WireGuard DNS: %w", err)
	}
	return nil
}
