package convert

import (
	"github.com/xiecang/node-quality/v2/internal/clash2singbox/model/clash"
	"github.com/xiecang/node-quality/v2/internal/clash2singbox/model/singbox"
)

func httpOpts(p *clash.Proxies, s *singbox.SingBoxOut) error {
	tls(p, s)
	s.Username = p.Username
	return nil
}

func socks5(p *clash.Proxies, s *singbox.SingBoxOut) error {
	tls(p, s)
	s.Username = p.Username
	return nil
}
