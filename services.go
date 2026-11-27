package nodequality

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"
)

func serviceBodyStatus(body []byte) (Status, string) {
	text := strings.ToLower(string(body))
	for _, marker := range []string{
		"you may be connected to a disallowed isp",
		"sorry, you have been blocked",
		"unsupported_country",
		"not available in your country",
		"not supported in your country",
		"country is not supported",
	} {
		if strings.Contains(text, marker) {
			return Fail, "service_region_blocked"
		}
	}
	for _, marker := range []string{"cf-chl-", "verify you are human", "just a moment...", "captcha"} {
		if strings.Contains(text, marker) {
			return Unknown, "service_challenge"
		}
	}
	return Pass, ""
}

func checkOptionalService(ctx context.Context, client *http.Client, check ServiceCheck, timeout time.Duration) ServiceResult {
	result := ServiceResult{
		Status:        Unknown,
		MeasuredAt:    time.Now(),
		CheckRevision: ServiceCheckRevision(check),
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, check.URL, nil)
	if err != nil {
		result.FailureCode = "invalid_url"
		return result
	}
	request.Header.Set("User-Agent", "Mozilla/5.0")
	response, err := client.Do(request)
	if err != nil {
		result.FailureCode = "network"
		return result
	}
	defer response.Body.Close()
	result.HTTPStatus = response.StatusCode
	if check.Kind == "imgur" && response.StatusCode == http.StatusOK && strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Type")), "image/") {
		result.Status = Pass
		return result
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 64<<10+1))
	if err != nil || len(body) > 64<<10 {
		result.FailureCode = "body_incomplete"
		return result
	}
	status, code := serviceBodyStatus(body)
	if status != Pass {
		result.Status = status
		result.FailureCode = code
		return result
	}
	switch check.Kind {
	case "openai-mobile":
		text := strings.ToLower(string(body))
		switch {
		case strings.Contains(text, "disallowed isp"), strings.Contains(text, "sorry, you have been blocked"):
			result.Status = Fail
			result.FailureCode = "service_blocked"
		case (response.StatusCode == http.StatusOK || response.StatusCode == http.StatusForbidden) && strings.Contains(text, "request is not allowed. please try again later."):
			result.Status = Pass
		default:
			result.FailureCode = "unexpected_service_response"
		}
	case "anthropic":
		text := strings.ToLower(string(body))
		switch {
		case response.StatusCode == http.StatusUnauthorized && strings.Contains(text, "authentication_error"):
			result.Status = Pass
		case response.StatusCode == http.StatusForbidden && strings.Contains(text, "forbidden"):
			result.Status = Fail
			result.FailureCode = "service_region_blocked"
		default:
			result.FailureCode = "unexpected_service_response"
		}
	default:
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			result.Status = Pass
		} else {
			result.FailureCode = "service_http_error"
		}
	}
	return result
}
