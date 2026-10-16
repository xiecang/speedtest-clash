package convert

import (
	"fmt"

	"github.com/xiecang/node-quality/v2/internal/clash2singbox/model"
	"github.com/xiecang/node-quality/v2/internal/clash2singbox/model/clash"
	"github.com/xiecang/node-quality/v2/internal/clash2singbox/model/singbox"
)

func anytls(p *clash.Proxies, s *singbox.SingBoxOut, v model.SingBoxVer) ([]singbox.SingBoxOut, error) {
	p.Tls = true
	tls(p, s)
	s.TcpFastOpen = false

	if p.IdleSessionCheckInterval != 0 {
		s.IdleSessionCheckInterval = fmt.Sprintf("%vs", p.IdleSessionCheckInterval)
	}
	if p.IdleSessionTimeout != 0 {
		s.IdleSessionTimeout = fmt.Sprintf("%vs", p.IdleSessionTimeout)
	}
	if p.MinIdleSession != 0 {
		s.MinIdleSession = int(p.MinIdleSession)
	}
	return []singbox.SingBoxOut{*s}, nil
}
