// SPDX-License-Identifier: GPL-3.0-only
package engine

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"

	mc "github.com/metacubex/mihomo/constant"
	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter"
	"github.com/xiecang/node-quality/v2/internal/model"
)

// Batch owns only this batch's native outbounds; it never activates TUN or inbounds.
type Batch struct {
	dialers   map[string]model.DialContext
	failures  map[string]error
	proxies   []mc.Proxy
	box       *box.Box
	endpoints []adapter.Endpoint
	closeOnce sync.Once
	closeErr  error
}

func OpenBatch(ctx context.Context, nodes []model.Node, core model.Core) (*Batch, error) {
	if err := model.ValidateDependencies(nodes); err != nil {
		return nil, err
	}
	b := &Batch{dialers: make(map[string]model.DialContext), failures: make(map[string]error)}
	var err error
	switch core {
	case model.Mihomo:
		err = b.openMihomo(nodes)
	case model.SingBox:
		err = b.openSingBox(ctx, nodes)
	default:
		err = fmt.Errorf("unsupported core %q", core)
	}
	if err != nil {
		return nil, errors.Join(sanitizeEngineError(err), b.Close())
	}
	return b, nil
}

func (b *Batch) Dialer(key string) (model.DialContext, error) {
	d, ok := b.dialers[key]
	if !ok {
		if err := b.failures[key]; err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("missing outbound %s", key)
	}
	return d, nil
}

func (b *Batch) Close() error {
	if b == nil {
		return nil
	}
	b.closeOnce.Do(func() {
		b.closeErr = closeSingBox(b.box, b.endpoints)
		for i := len(b.proxies) - 1; i >= 0; i-- {
			if err := b.proxies[i].Close(); err != nil && !errors.Is(err, os.ErrClosed) && !errors.Is(err, net.ErrClosed) {
				b.closeErr = errors.Join(b.closeErr, err)
			}
		}
	})
	return b.closeErr
}
