package models

import (
	"context"
	"time"

	"github.com/metacubex/mihomo/constant"
)

type SortField string

const (
	SortFieldBandwidth  SortField = "b"         // 带宽
	SortFieldBandwidth2 SortField = "bandwidth" // 带宽
	SortFieldTTFB       SortField = "t"         // 延迟
	SortFieldTTFB2      SortField = "ttfb"

	DefaultLivenessAddr = "https://speed.cloudflare.com/__down?bytes=%d"
)

type Cache interface {
	Get(ctx context.Context, key string) (*CProxyWithResult, bool)
	Set(ctx context.Context, key string, result *CProxyWithResult) error
	// GenerateKey 生成节点的唯一标识
	GenerateKey(proxy *CProxy) string
	Close() error
}

type Options struct {
	LivenessAddr        string        `json:"liveness_addr"`          // 测速时调用的地址，格式如 https://speed.cloudflare.com/__down?bytes=%d
	DownloadSize        int           `json:"download_size"`          // 测速时下载的文件大小，单位为 bit(使用默认cloudflare的话)，默认下载10M
	Timeout             time.Duration `json:"timeout"`                // 每个代理测速的超时时间
	ConfigPath          string        `json:"config_path"`            // 配置文件地址，可以为 URL 或者本地路径，多个使用 | 分隔
	NameRegexContain    string        `json:"name_regex_contain"`     // 通过名字过滤代理，只测试过滤部分，格式为正则，默认全部测
	NameRegexNonContain string        `json:"name_regex_not_contain"` // 通过名字过滤代理，跳过过滤部分，格式为正则
	SortField           SortField     `json:"sort_field"`             // 排序方式，b 带宽 t 延迟
	URLForTest          []string      `json:"url_for_test"`           // 测试 URL 是否可访问
	ProxyUrl            string        `json:"proxy_url"`              // ConfigPath 为网络链接时可使用指定代理下载
	CheckTypes          []CheckType   `json:"check_types"`            // 检查节点可解锁的类型, 可用值请参考 CheckType
	Concurrent          int           `json:"concurrent"`             // 测速的并发数，默认 CPU 数量*3
	Cache               Cache         `json:"-"`                      // 缓存实现，不序列化
}

type CProxy struct {
	constant.Proxy
	SecretConfig map[string]any
}

// https://wiki.metacubex.one/en/config/proxy-providers/
// proxy-providers:
//  provider1:
//    type: http
//    url: "http://test.com"
//    path: ./proxy_providers/provider1.yaml

type ProxyProvider struct {
	Url string `json:"url"`
	// 支持自定义 header，后续再加吧
}

type RawConfig struct {
	Providers map[string]ProxyProvider `yaml:"proxy-providers"`
	Proxies   []map[string]any         `yaml:"proxies"`
}
