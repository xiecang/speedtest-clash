// SPDX-License-Identifier: GPL-3.0-only
package measure

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/xiecang/node-quality/v2/internal/model"
)

// Optional service identity is independent of PlanRevision and core admission.
func ServiceCheckRevision(check model.ServiceCheck) string {
	sum := sha256.Sum256([]byte("service-check-v1\x00" + check.Kind + "\x00" + check.URL))
	return hex.EncodeToString(sum[:])
}

func validateServiceChecks(plan model.Plan) error {
	if len(plan.ServiceChecks) > 3 {
		return errors.New("at most three optional service checks")
	}
	names := map[string]bool{}
	for name := range plan.ServiceURLs {
		names[strings.ToLower(name)] = true
	}
	for name, check := range plan.ServiceChecks {
		if name == "" || len(name) > 64 || names[strings.ToLower(name)] {
			return errors.New("duplicate or invalid service ID")
		}
		names[strings.ToLower(name)] = true
		if check.Kind != "openai-mobile" && check.Kind != "http" && check.Kind != "imgur" {
			return errors.New("unsupported service checker")
		}
		u, err := url.Parse(check.URL)
		if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
			return errors.New("invalid service check URL")
		}
	}
	return nil
}

func checkOptionalService(ctx context.Context, client *http.Client, check model.ServiceCheck, timeout time.Duration) model.ServiceResult {
	result := model.ServiceResult{Status: model.Unknown, MeasuredAt: time.Now(), CheckRevision: ServiceCheckRevision(check)}
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
	if check.Kind == "imgur" && response.StatusCode == 200 && strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Type")), "image/") {
		result.Status = model.Pass
		return result
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 64<<10+1))
	if err != nil || len(body) > 64<<10 {
		result.FailureCode = "body_incomplete"
		return result
	}
	status, code := serviceBodyStatus(body)
	if status != model.Pass {
		result.Status = status
		result.FailureCode = code
		return result
	}
	if check.Kind == "openai-mobile" {
		text := strings.ToLower(string(body))
		switch {
		case strings.Contains(text, "disallowed isp"), strings.Contains(text, "sorry, you have been blocked"):
			result.Status = model.Fail
			result.FailureCode = "service_blocked"
		case (response.StatusCode == 200 || response.StatusCode == 403) && strings.Contains(text, "request is not allowed. please try again later."):
			result.Status = model.Pass
		default:
			result.FailureCode = "unexpected_service_response"
		}
	} else if response.StatusCode >= 200 && response.StatusCode < 300 {
		result.Status = model.Pass
	} else {
		result.FailureCode = "service_http_error"
	}
	return result
}
