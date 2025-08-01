package check

import (
	"context"
	"github.com/metacubex/mihomo/common/convert"
	C "github.com/metacubex/mihomo/constant"
	"github.com/xiecang/speedtest-clash/speedtest/models"
	"github.com/xiecang/speedtest-clash/speedtest/requests"
	"net/http"
	"strings"
	"time"
)

const (
	GPTTrace = "https://chat.openai.com/cdn-cgi/trace"

	timeout      = 30 * time.Second
	retryTimes   = 3
	retryTimeOut = 3 * time.Second
)

var gptSupportCountry = map[string]bool{
	"AL": true, "DZ": true, "AF": true, "AD": true, "AO": true, "AG": true, "AR": true,
	"AM": true, "AU": true, "AT": true, "AZ": true, "BS": true, "BH": true, "BD": true,
	"BB": true, "BE": true, "BZ": true, "BJ": true, "BT": true, "BO": true, "BA": true,
	"BW": true, "BR": true, "BN": true, "BG": true, "BF": true, "BI": true, "CV": true,
	"KH": true, "CM": true, "CA": true, "CF": true, "TD": true, "CL": true, "CO": true,
	"KM": true, "CG": true, "CD": true, "CR": true, "CI": true, "HR": true, "CY": true,
	"CZ": true, "DK": true, "DJ": true, "DM": true, "DO": true, "EC": true, "EG": true,
	"SV": true, "GQ": true, "ER": true, "EE": true, "SZ": true, "ET": true, "FJ": true,
	"FI": true, "FR": true, "GA": true, "GM": true, "GE": true, "DE": true, "GH": true,
	"GR": true, "GD": true, "GT": true, "GN": true, "GW": true, "GY": true, "HT": true,
	"VA": true, "HN": true, "HU": true, "IS": true, "IN": true, "ID": true, "IQ": true,
	"IE": true, "IL": true, "IT": true, "JM": true, "JP": true, "JO": true, "KZ": true,
	"KE": true, "KI": true, "KW": true, "KG": true, "LA": true, "LV": true, "LB": true,
	"LS": true, "LR": true, "LY": true, "LI": true, "LT": true, "LU": true, "MG": true,
	"MW": true, "MY": true, "MV": true, "ML": true, "MT": true, "MH": true, "MR": true,
	"MU": true, "MX": true, "FM": true, "MD": true, "MC": true, "MN": true, "ME": true,
	"MA": true, "MZ": true, "MM": true, "NA": true, "NR": true, "NP": true, "NL": true,
	"NZ": true, "NI": true, "NE": true, "NG": true, "MK": true, "NO": true, "OM": true,
	"PK": true, "PW": true, "PS": true, "PA": true, "PG": true, "PY": true, "PE": true,
	"PH": true, "PL": true, "PT": true, "QA": true, "RO": true, "RW": true, "KN": true,
	"LC": true, "VC": true, "WS": true, "SM": true, "ST": true, "SA": true, "SN": true,
	"RS": true, "SC": true, "SL": true, "SG": true, "SK": true, "SI": true, "SB": true,
	"SO": true, "ZA": true, "KR": true, "SS": true, "ES": true, "LK": true, "SR": true,
	"SE": true, "CH": true, "SD": true, "TW": true, "TJ": true, "TZ": true, "TH": true,
	"TL": true, "TG": true, "TO": true, "TT": true, "TN": true, "TR": true, "TM": true,
	"TV": true, "UG": true, "UA": true, "AE": true, "GB": true, "US": true, "UY": true,
	"UZ": true, "VU": true, "VN": true, "YE": true, "ZM": true, "ZW": true,
}

func getCountryCode(ctx context.Context, client *http.Client, url string) (string, error) {
	resp, err := requests.Request(ctx, &requests.RequestOption{
		Method:       http.MethodGet,
		URL:          url,
		Timeout:      timeout,
		RetryTimes:   retryTimes,
		RetryTimeOut: retryTimeOut,
		Client:       client,
		Headers: map[string]string{
			"User-Agent": convert.RandUserAgent(),
		},
	})
	if err != nil {
		return "", err
	}
	lines := strings.Split(string(resp.Body), "\n")
	var loc string
	for _, line := range lines {
		if !strings.Contains(line, "loc=") {
			continue
		}
		loc = strings.TrimPrefix(line, "loc=")
		break
	}
	return loc, nil
}

type gptWebChecker struct {
	tp models.CheckType
}

func NewGPTWebChecker() models.Checker {
	return &gptWebChecker{
		tp: models.CheckTypeGPTWeb,
	}
}

func (g *gptWebChecker) Check(ctx context.Context, proxy C.Proxy) (result models.CheckResult, err error) {
	client := requests.GetClient(proxy, timeout)

	loc, err := getCountryCode(ctx, client, GPTTrace)
	if err != nil {
		return models.NewCheckResult(g.tp, false, loc), err
	}

	//resp, err := requests.Request(ctx, &requests.RequestOption{
	//	Method:       http.MethodGet,
	//	URL:          "https://api.openai.com/compliance/cookie_requirements",
	//	Timeout:      timeout,
	//	RetryTimes:   retryTimes,
	//	RetryTimeOut: retryTimeOut,
	//	Client:       client,
	//})
	//if err != nil || resp.StatusCode != http.StatusOK {
	//	return models.NewCheckResult(g.tp, false, loc), err
	//}
	//bodyStr := strings.ToLower(string(resp.Body))
	//
	//return models.NewCheckResult(g.tp, strings.Contains(bodyStr, "unsupported_country"), loc), nil

	return models.NewCheckResult(g.tp, gptSupportCountry[loc], loc), err
}
