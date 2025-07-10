package speedtest

import (
	"encoding/json"
	"github.com/xiecang/speedtest-clash/speedtest/models"
	"net/url"
	"reflect"
	"testing"
	"time"
)

func Test_TestSpeed(t *testing.T) {
	type args struct {
		proxy   map[string]interface{}
		options *models.Options
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
				options: &models.Options{
					DownloadSize: 10 * 1024 * 1024,
					Timeout:      5 * time.Second,
					SortField:    models.SortFieldBandwidth,
					LivenessAddr: models.DefaultLivenessAddr,
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
					SortField:    models.SortFieldBandwidth,
					LivenessAddr: models.DefaultLivenessAddr,
					URLForTest:   []string{"https://www.google.com"},
					CheckTypes: []models.CheckType{
						models.CheckTypeGPTIOS,
						models.CheckTypeGPTWeb,
						models.CheckTypeGPTAndroid,
					},
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

func TestReadProxies(t *testing.T) {
	type args struct {
		configPathConfig string
		proxyUrl         string
	}
	tests := []struct {
		name    string
		args    args
		want    map[string]models.CProxy
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
