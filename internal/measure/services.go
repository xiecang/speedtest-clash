// SPDX-License-Identifier: GPL-3.0-only
package measure

import (
	"strings"

	"github.com/xiecang/node-quality/v2/internal/model"
)

// Known block and bot-challenge responses are not successful service checks.
func serviceBodyStatus(body []byte) (model.Status, string) {
	text := strings.ToLower(string(body))
	for _, marker := range []string{"you may be connected to a disallowed isp", "sorry, you have been blocked", "unsupported_country", "not available in your country", "not supported in your country", "country is not supported"} {
		if strings.Contains(text, marker) {
			return model.Fail, "service_region_blocked"
		}
	}
	for _, marker := range []string{"cf-chl-", "verify you are human", "just a moment...", "captcha"} {
		if strings.Contains(text, marker) {
			return model.Unknown, "service_challenge"
		}
	}
	return model.Pass, ""
}
