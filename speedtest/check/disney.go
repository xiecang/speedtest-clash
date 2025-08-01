package check

import (
	"context"
	"encoding/json"
	"fmt"
	C "github.com/metacubex/mihomo/constant"
	"github.com/xiecang/speedtest-clash/speedtest/models"
	"github.com/xiecang/speedtest-clash/speedtest/requests"
	"net/http"
	"strings"
)

const (
	cookie    = "grant_type=urn%3Aietf%3Aparams%3Aoauth%3Agrant-type%3Atoken-exchange&latitude=0&longitude=0&platform=browser&subject_token=DISNEYASSERTION&subject_token_type=urn%3Abamtech%3Aparams%3Aoauth%3Atoken-type%3Adevice"
	assertion = `{"deviceFamily":"browser","applicationRuntime":"chrome","deviceProfile":"windows","attributes":{}}`
	authBear  = "Bearer ZGlzbmV5JmJyb3dzZXImMS4wLjA.Cu56AgSfBTDag5NiRA81oLHkDZfu5L3CKadnefEAY84"
)

type disneyChecker struct {
	tp models.CheckType
}

func NewDisneyChecker() models.Checker {
	return &disneyChecker{
		tp: models.CheckTypeDisney,
	}
}

func (d *disneyChecker) Check(ctx context.Context, proxy C.Proxy) (result models.CheckResult, err error) {
	client := requests.GetClient(proxy, timeout)
	var errRes = models.NewCheckResult(d.tp, false, "")

	// 第一步：获取 assertion token
	assertionToken, err := d.getAssertionToken(ctx, client)
	if err != nil {
		return errRes, err
	}

	// 第二步：获取 access token
	refreshToken, err := d.getAccessToken(ctx, client, assertionToken)
	if err != nil {
		return errRes, err
	}

	// 第三步：检查区域
	inSupportedLocation, err := d.checkRegion(ctx, client, refreshToken)
	if err != nil {
		return errRes, err
	}
	return models.NewCheckResult(d.tp, inSupportedLocation, ""), nil
}

func (d *disneyChecker) getAssertionToken(ctx context.Context, client *http.Client) (string, error) {
	resp, err := requests.Request(ctx, &requests.RequestOption{
		Method:       http.MethodPost,
		URL:          "https://disney.api.edge.bamgrid.com/devices",
		Body:         []byte(assertion),
		Timeout:      timeout,
		RetryTimes:   retryTimes,
		RetryTimeOut: retryTimeOut,
		Client:       client,
		Headers: map[string]string{
			"User-Agent":    userAgent,
			"Authorization": authBear,
			"Content-Type":  "application/json",
		},
	})
	if err != nil {
		return "", err
	}

	var assertionResp map[string]interface{}
	if err := json.Unmarshal(resp.Body, &assertionResp); err != nil {
		return "", err
	}
	assertionToken, ok := assertionResp["assertion"].(string)
	if !ok {
		return "", fmt.Errorf("无法获取 assertion token")
	}
	return assertionToken, nil
}

func (d *disneyChecker) getAccessToken(ctx context.Context, client *http.Client, assertionToken string) (string, error) {
	tokenData := strings.Replace(cookie, "DISNEYASSERTION", assertionToken, 1)
	resp, err := requests.Request(ctx, &requests.RequestOption{
		Method:       http.MethodPost,
		URL:          "https://disney.api.edge.bamgrid.com/token",
		Body:         []byte(tokenData),
		Timeout:      timeout,
		RetryTimes:   retryTimes,
		RetryTimeOut: retryTimeOut,
		Client:       client,
		Headers: map[string]string{
			"User-Agent":    userAgent,
			"Authorization": authBear,
			"Content-Type":  "application/x-www-form-urlencoded",
		},
	})
	if err != nil {
		return "", err
	}

	var tokenResp map[string]interface{}
	if err := json.Unmarshal(resp.Body, &tokenResp); err != nil {
		return "", err
	}

	if errDesc, ok := tokenResp["error_description"].(string); ok && errDesc == "forbidden-location" {
		return "", nil
	}

	refreshToken, ok := tokenResp["refresh_token"].(string)
	if !ok {
		return "", nil
	}
	return refreshToken, nil
}

func (d *disneyChecker) checkRegion(ctx context.Context, client *http.Client, refreshToken string) (bool, error) {
	gqlQuery := fmt.Sprintf(`{"query":"mutation refreshToken($input: RefreshTokenInput!) {refreshToken(refreshToken: $input) {activeSession {sessionId}}}","variables":{"input":{"refreshToken":"%s"}}}`, refreshToken)
	resp, err := requests.Request(ctx, &requests.RequestOption{
		Method:       http.MethodPost,
		URL:          "https://disney.api.edge.bamgrid.com/graph/v1/device/graphql",
		Body:         []byte(gqlQuery),
		Timeout:      timeout,
		RetryTimes:   1,
		RetryTimeOut: retryTimeOut,
		Client:       client,
		Headers: map[string]string{
			"User-Agent":    userAgent,
			"Authorization": authBear,
		},
	})
	if err != nil {
		return false, err
	}
	var gqlResp map[string]interface{}
	if err := json.Unmarshal(resp.Body, &gqlResp); err != nil {
		return false, err
	}

	// 检查区域信息
	extensions, ok := gqlResp["extensions"].(map[string]interface{})
	if !ok {
		return false, err
	}

	sdk, ok := extensions["sdk"].(map[string]interface{})
	if !ok {
		return false, err
	}

	session, ok := sdk["session"].(map[string]interface{})
	if !ok {
		return false, err
	}

	inSupportedLocation, _ := session["inSupportedLocation"].(bool)
	return inSupportedLocation, nil
}
