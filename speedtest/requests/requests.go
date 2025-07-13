package requests

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/metacubex/mihomo/log"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

type RequestOption struct {
	Method  string
	URL     string
	Body    []byte
	JSON    interface{} // 传递这个参数可以为任意可 json 序列化值，会自动加上 Content-Type, 并覆盖 Body 内容
	Headers map[string]string
	Timeout time.Duration // 单次请求超时时间
	// 重试相关
	RetryTimes   int           // 重试次数
	RetryTimeOut time.Duration // 重试超时时间
	// 详细日志
	Verbose bool
	// 代理链接
	ProxyUrl *url.URL
	Client   *http.Client
}

type XcResponse struct {
	Body       []byte
	StatusCode int
}

func checkedOption(option *RequestOption) (*RequestOption, error) {
	if option.Method == "" {
		option.Method = http.MethodGet
	}
	if option.Headers == nil {
		option.Headers = make(map[string]string)
	}
	if option.JSON != nil {
		option.Headers["Content-Type"] = "application/json"
		var err error
		option.Body, err = json.Marshal(option.JSON)
		if err != nil {
			return nil, err
		}
	}
	if option.Timeout == 0 {
		option.Timeout = 5 * time.Second
	}
	return option, nil
}

func request(ctx context.Context, option *RequestOption) (*XcResponse, error) {
	var (
		req *http.Request
		err error
	)
	option, err = checkedOption(option)
	if err != nil {
		if option.Verbose {
			log.Errorln("checkedOption error: %v", err)
		}
		return nil, fmt.Errorf("checkedOption error: %w", err)
	}
	if option.Verbose {
		log.Infoln(requestCurl(option))
	}

	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout: option.Timeout,
		}).DialContext,
	}
	if option.ProxyUrl != nil {
		transport.Proxy = http.ProxyURL(option.ProxyUrl)
	}

	client := option.Client
	if client == nil {
		client = &http.Client{
			Transport: transport,
			Timeout:   option.Timeout,
		}
	}

	req, err = http.NewRequestWithContext(ctx, option.Method, option.URL, bytes.NewReader(option.Body))
	if err != nil {
		if option.Verbose {
			log.Errorln("http.NewRequest error: %v", err)
		}
		return nil, fmt.Errorf("http.NewRequest error: %w", err)
	}
	for k, v := range option.Headers {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		if option.Verbose {
			log.Errorln("client.Do error: %v", err)
		}
		return nil, fmt.Errorf("client.Do error: %w", err)
	}
	defer resp.Body.Close()

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var body []byte
	body, err = io.ReadAll(resp.Body)
	return &XcResponse{
		Body:       body,
		StatusCode: resp.StatusCode,
	}, nil
}

func Request(ctx context.Context, option *RequestOption) (*XcResponse, error) {
	resp, err := request(ctx, option)
	if option.RetryTimes > 0 && (err != nil || resp == nil) {
		var timeout = option.RetryTimeOut
		if timeout <= 0 {
			timeout = 3 * time.Second
		}
		for i := 0; i < option.RetryTimes; i++ {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			time.Sleep(timeout)
			var t = time.Second + timeout
			timeout = t
			resp, err = request(ctx, option)
			if resp != nil {
				break
			}
		}
	}
	return resp, err
}

func requestCurl(option *RequestOption) string {
	var curl = fmt.Sprintf("curl -X %s '%s'", option.Method, option.URL)
	for k, v := range option.Headers {
		curl += fmt.Sprintf(" -H '%s: %s'", k, v)
	}
	if option.Body != nil {
		curl += fmt.Sprintf(" -d '%s'", string(option.Body))
	}
	return curl
}
