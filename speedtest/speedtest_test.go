package speedtest

import (
	"encoding/json"
	"github.com/metacubex/mihomo/adapter"
	"net/url"
	"reflect"
	"testing"
	"time"
)

func Test_TestSpeed(t *testing.T) {
	type args struct {
		proxy   map[string]interface{}
		options *Options
	}
	tests := []struct {
		name    string
		args    args
		want    []Result
		wantErr bool
	}{
		{
			name: "ssr error",
			args: args{
				proxy: map[string]interface{}{
					"cipher":         "aes-256-cfb",
					"name":           "ssr",
					"obfs":           "plain",
					"obfs-param":     "",
					"password":       "S7KwUu7yBy58S3Ga",
					"port":           9042,
					"protocol":       "origin",
					"protocol-param": "",
					"server":         "103.172.116.79",
					"type":           "ssr",
					"udp":            true,
				},
				options: &Options{
					DownloadSize: 10 * 1024 * 1024,
					Timeout:      5 * time.Second,
					SortField:    SortFieldBandwidth,
					LivenessAddr: DefaultLivenessAddr,
				},
			},
			want: []Result{
				{
					Name: "ssr",
				},
			},
		},
		{
			name: "test url google",
			args: args{
				proxy: map[string]interface{}{
					"name": "url-test",
					// fill in the rest of the fields
				},
				options: &Options{
					DownloadSize: 10 * 1024 * 1024,
					Timeout:      5 * time.Second,
					SortField:    SortFieldBandwidth,
					LivenessAddr: DefaultLivenessAddr,
					URLForTest:   []string{"https://www.google.com"},
				},
			},
			want: []Result{
				{
					Name: "url-test",
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bytes, err := json.Marshal(map[string]interface{}{
				"proxies": []map[string]interface{}{tt.args.proxy},
			})
			if err != nil {
				t.Errorf("Invalid proxy: %v", err)
				return
			}
			lps, err := loadProxies(bytes, nil)
			if err != nil {
				t.Errorf("Invalid proxy: %v", err)
				return
			}
			got, err := TestSpeed(lps, tt.args.options)
			if (err != nil) != tt.wantErr {
				t.Errorf("TestSpeed() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("TestSpeed() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_testChatGPTAccessWeb(t *testing.T) {
	type args struct {
		config  map[string]any
		timeout time.Duration
	}
	tests := []struct {
		name string
		args args
		want GPTResult
	}{
		{
			name: "1",
			args: args{
				config: map[string]any{
					"name":             "gpt",
					"server":           "120.234.147.175",
					"port":             41492,
					"type":             "vmess",
					"uuid":             "418048af-a293-4b99-9b0c-98ca3580dd24",
					"alterId":          64,
					"cipher":           "auto",
					"tls":              false,
					"skip-cert-verify": true,
					"udp":              true,
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proxy, err := adapter.ParseProxy(tt.args.config)
			if err != nil {
				t.Errorf("parse config failed: %v", err)
				return
			}
			if got := TestChatGPTAccess(proxy, tt.args.timeout); got != tt.want {
				t.Errorf("testChatGPTAccessWeb() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestReadProxies(t *testing.T) {
	type args struct {
		configPathConfig string
		proxyUrl         string
	}
	tests := []struct {
		name    string
		args    args
		want    map[string]CProxy
		wantErr bool
	}{
		{
			name: "test provider",
			args: args{
				configPathConfig: "test.yaml",
				proxyUrl:         "http://127.0.0.1:7890",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proxyUrl, err := url.Parse(tt.args.proxyUrl)
			if (err != nil) != tt.wantErr {
				t.Errorf("ReadProxies() Parse proxyUrl error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			got, err := ReadProxies(tt.args.configPathConfig, proxyUrl)
			if (err != nil) != tt.wantErr {
				t.Errorf("ReadProxies() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ReadProxies() got = %v, want %v", got, tt.want)
			}
		})
	}
}
