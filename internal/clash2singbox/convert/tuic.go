package convert

import (
	"fmt"

	"github.com/xiecang/node-quality/v2/internal/clash2singbox/model"
	"github.com/xiecang/node-quality/v2/internal/clash2singbox/model/clash"
	"github.com/xiecang/node-quality/v2/internal/clash2singbox/model/singbox"
)

func tuic(p *clash.Proxies, s *singbox.SingBoxOut, _ model.SingBoxVer) ([]singbox.SingBoxOut, error) {
	p.Tls = true
	tls(p, s)
	s.UUID = p.Uuid
	s.CongestionController = p.CongestionController
	s.UdpRelayMode = p.UdpRelayMode
	s.UdpOverStream = bool(p.UdpOverStream)
	s.ZeroRttHandshake = bool(p.ReduceRtt)
	if p.HeartbeatInterval != 0 {
		s.Heartbeat = fmt.Sprintf("%vms", p.HeartbeatInterval)
	}
	if p.IP != "" {
		s.Server = p.IP
	}
	return []singbox.SingBoxOut{*s}, nil
}
