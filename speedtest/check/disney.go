package check

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	C "github.com/metacubex/mihomo/constant"
	"github.com/xiecang/speedtest-clash/speedtest/models"
	"github.com/xiecang/speedtest-clash/speedtest/requests"
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
	client := requests.GetClient(proxy, 30*time.Second)
	var errRes = models.NewCheckResult(d.tp, false, "")

	// 第一步：获取 assertion token
	assertionToken, err := d.getAssertionToken(ctx, client)
	if err != nil {
		return errRes, err
	}

	// 第二步：获取 access token
	refreshToken, forbidden, err := d.getAccessToken(ctx, client, assertionToken)
	if err != nil {
		return errRes, err
	}
	if forbidden || refreshToken == "" {
		return errRes, nil
	}

	// 第三步：检查区域
	inSupportedLocation, region, err := d.checkRegion(ctx, client, refreshToken)
	if err != nil {
		return errRes, err
	}
	return models.NewCheckResult(d.tp, inSupportedLocation, region), nil
}

func (d *disneyChecker) getAssertionToken(ctx context.Context, client *http.Client) (string, error) {
	resp, err := requests.Request(ctx, &requests.RequestOption{
		Method:       http.MethodPost,
		URL:          "https://disney.api.edge.bamgrid.com/devices",
		Body:         []byte(assertion),
		Timeout:      30 * time.Second,
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

func (d *disneyChecker) getAccessToken(ctx context.Context, client *http.Client, assertionToken string) (string, bool, error) {
	tokenData := strings.Replace(cookie, "DISNEYASSERTION", assertionToken, 1)
	resp, err := requests.Request(ctx, &requests.RequestOption{
		Method:       http.MethodPost,
		URL:          "https://disney.api.edge.bamgrid.com/token",
		Body:         []byte(tokenData),
		Timeout:      30 * time.Second,
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
		return "", false, err
	}

	refreshToken, forbidden, _ := parseDisneyTokenResponse(resp.Body)
	return refreshToken, forbidden, nil
}

func (d *disneyChecker) checkRegion(ctx context.Context, client *http.Client, refreshToken string) (bool, string, error) {
	gqlQuery := fmt.Sprintf(`{"query":"mutation refreshToken($input: RefreshTokenInput!) {refreshToken(refreshToken: $input) {activeSession {sessionId}}}","variables":{"input":{"refreshToken":"%s"}}}`, refreshToken)
	resp, err := requests.Request(ctx, &requests.RequestOption{
		Method:       http.MethodPost,
		URL:          "https://disney.api.edge.bamgrid.com/graph/v1/device/graphql",
		Body:         []byte(gqlQuery),
		Timeout:      30 * time.Second,
		RetryTimes:   1,
		RetryTimeOut: retryTimeOut,
		Client:       client,
		Headers: map[string]string{
			"User-Agent":    userAgent,
			"Authorization": authBear,
			"Content-Type":  "application/json",
		},
	})
	if err != nil {
		return false, "", err
	}
	region, inSupportedLocation, ok := parseDisneyGraphQLRegion(resp.Body)
	if ok {
		if region == "JP" {
			return true, region, nil
		}
		if d.disneyPreviewUnavailable(ctx, client) {
			return false, "", nil
		}
		return inSupportedLocation, region, nil
	}

	if region, ok := d.regionFromMainPage(ctx, client); ok {
		return true, region, nil
	}
	return false, "", nil
}

func (d *disneyChecker) disneyPreviewUnavailable(ctx context.Context, client *http.Client) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://disneyplus.com", nil)
	if err != nil {
		return true
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return true
	}
	defer resp.Body.Close()
	return isDisneyPreviewUnavailableURL(resp.Request.URL.String())
}

func (d *disneyChecker) regionFromMainPage(ctx context.Context, client *http.Client) (string, bool) {
	resp, err := requests.Request(ctx, &requests.RequestOption{
		Method:       http.MethodGet,
		URL:          "https://www.disneyplus.com/",
		Timeout:      30 * time.Second,
		RetryTimes:   retryTimes,
		RetryTimeOut: retryTimeOut,
		Client:       client,
		Headers: map[string]string{
			"User-Agent": userAgent,
		},
	})
	if err != nil {
		return "", false
	}
	return parseDisneyMainPageRegion(resp.Body)
}

func parseDisneyTokenResponse(body []byte) (refreshToken string, forbidden bool, found bool) {
	bodyStr := strings.ToLower(string(body))
	if strings.Contains(bodyStr, "forbidden-location") || strings.Contains(bodyStr, "403 error") {
		return "", true, false
	}

	var tokenResp map[string]interface{}
	if err := json.Unmarshal(body, &tokenResp); err == nil {
		if refreshToken, ok := tokenResp["refresh_token"].(string); ok && refreshToken != "" {
			return refreshToken, false, true
		}
	}

	re := regexp.MustCompile(`"refresh_token"\s*:\s*"([^"]+)"`)
	if match := re.FindSubmatch(body); len(match) == 2 {
		return string(match[1]), false, true
	}
	return "", false, false
}

func parseDisneyGraphQLRegion(body []byte) (region string, inSupportedLocation bool, ok bool) {
	regionRe := regexp.MustCompile(`"countryCode"\s*:\s*"([^"]+)"`)
	regionMatch := regionRe.FindSubmatch(body)
	if len(regionMatch) != 2 {
		return "", false, false
	}

	supportedRe := regexp.MustCompile(`"inSupportedLocation"\s*:\s*(false|true)`)
	supportedMatch := supportedRe.FindSubmatch(body)
	if len(supportedMatch) != 2 {
		return string(regionMatch[1]), false, false
	}
	return string(regionMatch[1]), string(supportedMatch[1]) == "true", true
}

func parseDisneyMainPageRegion(body []byte) (string, bool) {
	re := regexp.MustCompile(`"region"\s*:\s*"([^"]+)"`)
	match := re.FindSubmatch(body)
	if len(match) != 2 {
		return "", false
	}
	return string(match[1]), true
}

func isDisneyPreviewUnavailableURL(url string) bool {
	return strings.Contains(url, "preview") || strings.Contains(url, "unavailable")
}
