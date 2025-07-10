package check

import (
	"context"
	"encoding/json"
	C "github.com/metacubex/mihomo/constant"
	"github.com/xiecang/speedtest-clash/speedtest/models"
	"github.com/xiecang/speedtest-clash/speedtest/requests"
	"net/http"
	"strings"
	"time"
)

const (
	GPTTestURLAndroid = "https://android.chat.openai.com"
	errMsg            = "Something went wrong. You may be connected to a disallowed ISP. "
)

type response struct {
	CFDetails string `json:"cf_details"`
}

func requestChatGPT(ctx context.Context, client *http.Client, url string) (*response, error) {
	resp, err := requests.Request(ctx, &requests.RequestOption{
		Method:       http.MethodPost,
		URL:          url,
		Headers:      map[string]string{"Content-Type": "application/json"},
		Timeout:      60 * time.Second,
		RetryTimes:   3,
		RetryTimeOut: 3 * time.Second,
		Client:       client,
	})
	if err != nil {
		return nil, err
	}

	var data response
	if err = json.Unmarshal(resp.Body, &data); err != nil {
		return nil, err
	}
	return &data, err
}

type gptAndroidChecker struct {
	tp models.CheckType
}

func NewGPTAndroidChecker() models.Checker {
	return &gptAndroidChecker{
		tp: models.CheckTypeGPTAndroid,
	}
}

func (g *gptAndroidChecker) Check(ctx context.Context, proxy C.Proxy) (result models.CheckResult, err error) {

	client := requests.GetClient(proxy, timeout)

	// ● 若不可用会提示
	// {"cf_details":"Something went wrong. You may be connected to a disallowed ISP. If you are using VPN, try disabling it. Otherwise try a different Wi-Fi network or data connection."}
	//
	// ● 可用提示
	// {"cf_details":"Request is not allowed. Please try again later.", "type":"dc"}
	resp, err := requestChatGPT(ctx, client, GPTTestURLAndroid)
	if err != nil {
		return models.NewCheckResult(g.tp, false, ""), err
	}
	if !strings.Contains(resp.CFDetails, errMsg) {
		return models.NewCheckResult(g.tp, true, ""), err
	}

	return models.NewCheckResult(g.tp, false, ""), err
}
