package requests

import (
	"context"
	"github.com/metacubex/mihomo/constant"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func GetClient(proxy constant.Proxy, timeout time.Duration) *http.Client {
	client := http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				host, port, err := net.SplitHostPort(addr)
				if err != nil {
					return nil, err
				}
				// trim FQDN (#737)
				host = strings.TrimRight(host, ".")

				var u16Port uint16
				if port, err := strconv.ParseUint(port, 10, 16); err == nil {
					u16Port = uint16(port)
				}
				return proxy.DialContext(ctx, &constant.Metadata{
					Host:    host,
					DstPort: u16Port,
				})
			},
		},
	}
	return &client
}
