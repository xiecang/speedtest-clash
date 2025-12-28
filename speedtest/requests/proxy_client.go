package requests

import (
	"context"
	"net"
	"net/http"
	"time"

	"github.com/metacubex/mihomo/constant"
)

func GetClient(proxy constant.Proxy, timeout time.Duration) *http.Client {
	client := http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				metadata := constant.Metadata{}
				err := metadata.SetRemoteAddress(addr)
				if err != nil {
					return nil, err
				}

				return proxy.DialContext(ctx, &metadata)
			},
		},
	}
	return &client
}
