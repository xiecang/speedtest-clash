package speedtest

import (
	"fmt"
	C "github.com/metacubex/mihomo/constant"
	"regexp"
	"strings"
	"time"
)

type SortField string

const (
	SortFieldBandwidth  SortField = "b"         // 带宽
	SortFieldBandwidth2 SortField = "bandwidth" // 带宽
	SortFieldTTFB       SortField = "t"         // 延迟
	SortFieldTTFB2      SortField = "ttfb"

	GPTTestURLAndroid = "https://android.chat.openai.com"
	GPTTestURLIOS     = "https://ios.chat.openai.com/"
	GPTTestURLWeb     = "https://chatgpt.com/"
	GPTTrace          = "https://chat.openai.com/cdn-cgi/trace"

	errMsg              = "Something went wrong. You may be connected to a disallowed ISP. "
	DefaultLivenessAddr = "https://speed.cloudflare.com/__down?bytes=%d"
)

var (
	gptSupportCountry = []string{"AL", "DZ", "AF", "AD", "AO", "AG", "AR", "AM", "AU", "AT", "AZ", "BS", "BH", "BD", "BB", "BE", "BZ", "BJ", "BT", "BO", "BA", "BW", "BR", "BN", "BG", "BF", "BI", "CV", "KH", "CM", "CA", "CF", "TD", "CL", "CO", "KM", "CG", "CD", "CR", "CI", "HR", "CY", "CZ", "DK", "DJ", "DM", "DO", "EC", "EG", "SV", "GQ", "ER", "EE", "SZ", "ET", "FJ", "FI", "FR", "GA", "GM", "GE", "DE", "GH", "GR", "GD", "GT", "GN", "GW", "GY", "HT", "VA", "HN", "HU", "IS", "IN", "ID", "IQ", "IE", "IL", "IT", "JM", "JP", "JO", "KZ", "KE", "KI", "KW", "KG", "LA", "LV", "LB", "LS", "LR", "LY", "LI", "LT", "LU", "MG", "MW", "MY", "MV", "ML", "MT", "MH", "MR", "MU", "MX", "FM", "MD", "MC", "MN", "ME", "MA", "MZ", "MM", "NA", "NR", "NP", "NL", "NZ", "NI", "NE", "NG", "MK", "NO", "OM", "PK", "PW", "PS", "PA", "PG", "PY", "PE", "PH", "PL", "PT", "QA", "RO", "RW", "KN", "LC", "VC", "WS", "SM", "ST", "SA", "SN", "RS", "SC", "SL", "SG", "SK", "SI", "SB", "SO", "ZA", "KR", "SS", "ES", "LK", "SR", "SE", "CH", "SD", "TW", "TJ", "TZ", "TH", "TL", "TG", "TO", "TT", "TN", "TR", "TM", "TV", "UG", "UA", "AE", "GB", "US", "UY", "UZ", "VU", "VN", "YE", "ZM", "ZW"}
	red               = "\033[31m"
	green             = "\033[32m"
	emojiRegex        = regexp.MustCompile(`[\x{1F600}-\x{1F64F}\x{1F300}-\x{1F5FF}\x{1F680}-\x{1F6FF}\x{2600}-\x{26FF}\x{1F1E0}-\x{1F1FF}]`)
	spaceRegex        = regexp.MustCompile(`\s{2,}`)
)

type Options struct {
	LivenessAddr string        `json:"liveness_addr"` // 测速时调用的地址，格式如 https://speed.cloudflare.com/__down?bytes=%d
	DownloadSize int           `json:"download_size"` // 测速时下载的文件大小，单位为 bit(使用默认cloudflare的话)，默认下载10M
	Timeout      time.Duration `json:"timeout"`       // 每个代理测速的超时时间
	ConfigPath   string        `json:"config_path"`   // 配置文件地址，可以为 URL 或者本地路径，多个使用 | 分隔
	FilterRegex  string        `json:"filter_regex"`  // 通过名字过滤代理，只测试过滤部分，格式为正则，默认全部测
	SortField    SortField     `json:"sort_field"`    // 排序方式，b 带宽 t 延迟
	Concurrent   int           `json:"concurrent"`    // 测速时候的下载并发数
	TestGPT      bool          `json:"test_gpt"`      // 是否检测节点支持 GPT
	URLForTest   []string      `json:"url_for_test"`  // 测试 URL 是否可访问
	ProxyUrl     string        `json:"proxy_url"`     // ConfigPath 为网络链接时可使用指定代理下载
}

type CProxy struct {
	C.Proxy
	SecretConfig map[string]any
}

type GPTResult struct {
	Android bool   `json:"android"`
	IOS     bool   `json:"ios"`
	Web     bool   `json:"web"`
	Loc     string `json:"loc"`
}

type Result struct {
	Name      string
	Bandwidth float64
	TTFB      time.Duration
	Delay     uint16
	GPTResult
	URLForTest map[string]bool
}

func (r *Result) Alive() bool {
	return (r.Delay > 0) || (r.Bandwidth > 0 && r.TTFB > 0)
}

func formatName(name string) string {
	noEmoji := emojiRegex.ReplaceAllString(name, "")
	mergedSpaces := spaceRegex.ReplaceAllString(noEmoji, " ")
	return strings.TrimSpace(mergedSpaces)
}

func formatBandwidth(v float64) string {
	if v <= 0 {
		return "N/A"
	}
	if v < 1024 {
		return fmt.Sprintf("%.02fB/s", v)
	}
	v /= 1024
	if v < 1024 {
		return fmt.Sprintf("%.02fKB/s", v)
	}
	v /= 1024
	if v < 1024 {
		return fmt.Sprintf("%.02fMB/s", v)
	}
	v /= 1024
	if v < 1024 {
		return fmt.Sprintf("%.02fGB/s", v)
	}
	v /= 1024
	return fmt.Sprintf("%.02fTB/s", v)
}

func formatMilliseconds(v time.Duration) string {
	if v <= 0 {
		return "N/A"
	}
	return fmt.Sprintf("%.02fms", float64(v.Milliseconds()))
}

func formatGPT(v bool) string {
	if v {
		return "是"
	}
	return "否"
}

func (r *Result) Printf(format string) {
	color := ""
	if r.Bandwidth < 1024*1024 {
		color = red
	} else if r.Bandwidth > 1024*1024*10 {
		color = green
	}
	fmt.Printf(format, color, formatName(r.Name), formatBandwidth(r.Bandwidth), formatMilliseconds(r.TTFB),
		formatGPT(r.GPTResult.Android), formatGPT(r.GPTResult.IOS), formatGPT(r.GPTResult.Web),
		fmt.Sprintf("%d", r.Delay),
	)
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

type CProxyWithResult struct {
	Proxies CProxy
	Results Result
}
