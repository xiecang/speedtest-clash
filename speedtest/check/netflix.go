package check

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	C "github.com/metacubex/mihomo/constant"
	"github.com/xiecang/speedtest-clash/speedtest/models"
	"github.com/xiecang/speedtest-clash/speedtest/requests"
)

type netflixChecker struct {
	tp models.CheckType
}

func NewNetflixChecker() models.Checker {
	return &netflixChecker{
		tp: models.CheckTypeNetflix,
	}
}

func (n netflixChecker) Check(ctx context.Context, proxy C.Proxy) (result models.CheckResult, err error) {
	client := requests.GetClient(proxy, 30*time.Second)
	if country, ok, err := n.checkFastAPI(ctx, client); err != nil {
		return models.NewCheckResult(n.tp, false, ""), err
	} else if ok {
		return models.NewCheckResult(n.tp, true, country), nil
	}

	status1, err := n.requestStatus(ctx, client, "https://www.netflix.com/title/81280792")
	if err != nil {
		return models.NewCheckResult(n.tp, false, ""), err
	}
	status2, err := n.requestStatus(ctx, client, "https://www.netflix.com/title/70143836")
	if err != nil {
		return models.NewCheckResult(n.tp, false, ""), err
	}
	return models.NewCheckResult(n.tp, classifyNetflixTitleStatus(status1, status2), ""), nil
}

func (n netflixChecker) checkFastAPI(ctx context.Context, client *http.Client) (country string, ok bool, err error) {
	resp, err := requests.Request(ctx, &requests.RequestOption{
		Method:       http.MethodGet,
		URL:          "https://api.fast.com/netflix/speedtest/v2?https=true&token=YXNkZmFzZGxmbnNkYWZoYXNkZmhrYWxm&urlCount=5",
		Timeout:      30 * time.Second,
		RetryTimes:   retryTimes,
		RetryTimeOut: retryTimeOut,
		Client:       client,
		Headers: map[string]string{
			"User-Agent": userAgent,
		},
	})
	if err != nil {
		return "", false, err
	}
	if resp.StatusCode == http.StatusForbidden {
		return "", false, nil
	}
	country, ok = parseNetflixFastCountry(resp.Body)
	return country, ok, nil
}

func (n netflixChecker) requestStatus(ctx context.Context, client *http.Client, url string) (int, error) {
	resp, err := requests.Request(ctx, &requests.RequestOption{
		Method:       http.MethodGet,
		URL:          url,
		Timeout:      30 * time.Second,
		RetryTimes:   retryTimes,
		RetryTimeOut: retryTimeOut,
		Client:       client,
		Headers: map[string]string{
			"User-Agent": userAgent,
		},
	})
	if err != nil {
		return 0, err
	}
	return resp.StatusCode, nil
}

func parseNetflixFastCountry(body []byte) (string, bool) {
	var data struct {
		Targets []struct {
			Location struct {
				Country string `json:"country"`
			} `json:"location"`
		} `json:"targets"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return "", false
	}
	if len(data.Targets) == 0 || data.Targets[0].Location.Country == "" {
		return "", false
	}
	return data.Targets[0].Location.Country, true
}

func classifyNetflixTitleStatus(status1, status2 int) bool {
	if status1 == http.StatusForbidden || status2 == http.StatusForbidden {
		return false
	}
	if status1 == http.StatusOK || status1 == http.StatusMovedPermanently ||
		status2 == http.StatusOK || status2 == http.StatusMovedPermanently {
		return true
	}
	return false
}
