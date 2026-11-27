package engine

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"

	mc "github.com/metacubex/mihomo/constant"
	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter"
	"github.com/xiecang/node-quality/v2/codec"
)

const (
	MihomoVersion  = "1.19.32"
	SingBoxVersion = "1.14.2"
)

type Core string

const (
	Mihomo  Core = "mihomo"
	SingBox Core = "sing-box"
)

type DialContext func(context.Context, string, string) (net.Conn, error)

type BuildInfo struct {
	Profiles      []string `json:"profiles"`
	SchemaVersion int      `json:"schema_version"`
	Mihomo        string   `json:"mihomo"`
	SingBox       string   `json:"sing_box"`
	Go            string   `json:"go"`
	OS            string   `json:"os"`
	Arch          string   `json:"arch"`
	Tags          []string `json:"tags"`
}

type RelayInfo struct {
	SupportsTCP bool `json:"supports_tcp" yaml:"supports_tcp"`
	SupportsUDP bool `json:"supports_udp" yaml:"supports_udp"`
	RequiresUDP bool `json:"requires_udp" yaml:"requires_udp"`
}

func Capabilities() BuildInfo {
	result := BuildInfo{
		Profiles:      []string{"general", "ai", "availability", "services"},
		SchemaVersion: codec.SchemaVersion,
		Mihomo:        MihomoVersion,
		SingBox:       SingBoxVersion,
		Go:            runtime.Version(),
		OS:            runtime.GOOS,
		Arch:          runtime.GOARCH,
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "-tags" {
				result.Tags = strings.Split(setting.Value, ",")
			}
		}
	}
	return result
}

// Batch owns only a single batch's native outbounds; it never activates TUN or listeners.
type Batch struct {
	dialers   map[string]DialContext
	failures  map[string]error
	proxies   []mc.Proxy
	box       *box.Box
	endpoints []adapter.Endpoint
	closeOnce sync.Once
	closeErr  error
}

func OpenBatch(ctx context.Context, nodes []codec.Node, core Core) (*Batch, error) {
	if err := codec.ValidateDependencies(nodes); err != nil {
		return nil, err
	}
	b := &Batch{
		dialers:  make(map[string]DialContext, len(nodes)),
		failures: make(map[string]error),
	}
	var err error
	switch core {
	case Mihomo:
		err = b.openMihomo(nodes)
	case SingBox:
		err = b.openSingBox(ctx, nodes)
	default:
		err = fmt.Errorf("unsupported core %q", core)
	}
	if err != nil {
		return nil, errors.Join(sanitizeEngineError(err), b.Close())
	}
	return b, nil
}

func (b *Batch) Dialer(key string) (DialContext, error) {
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
