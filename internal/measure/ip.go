// SPDX-License-Identifier: GPL-3.0-only
package measure

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/xiecang/node-quality/v2/internal/model"
)

func queryIP(ctx context.Context, client *http.Client, plan model.Plan) []model.IPResult {
	if len(plan.IPProviders) == 0 {
		return []model.IPResult{{Provider: "unconfigured", Status: model.Unknown, MeasuredAt: time.Now()}}
	}
	var exit string
	var exitFamily int
	if plan.ExitIPURL != "" {
		func() {
			ctx, cancel := context.WithTimeout(ctx, plan.ProbeTimeout)
			defer cancel()
			request, err := http.NewRequestWithContext(ctx, "GET", plan.ExitIPURL, nil)
			if err != nil {
				return
			}
			response, err := client.Do(request)
			if err != nil {
				return
			}
			defer response.Body.Close()
			var data struct {
				IP string `json:"ip"`
			}
			if response.StatusCode == 200 && json.NewDecoder(io.LimitReader(response.Body, 1024)).Decode(&data) == nil {
				if address, err := netip.ParseAddr(data.IP); err == nil && address.IsGlobalUnicast() && !address.IsPrivate() {
					exit = address.String()
					exitFamily = 6
					if address.Is4() {
						exitFamily = 4
					}
				}
			}
		}()
	}
	results := make([]model.IPResult, 0, len(plan.IPProviders))
	fresh := make([]bool, 0, len(plan.IPProviders))
	for provider, address := range plan.IPProviders {
		revision := fmt.Sprintf("%x", sha256.Sum256([]byte("ip-check-v1\x00"+address)))
		result := model.IPResult{CheckRevision: revision, Provider: provider, Status: model.Unknown, MeasuredAt: time.Now()}
		if exit != "" && plan.LookupIP != nil {
			cached, ok, err := plan.LookupIP(ctx, exit, exitFamily, provider+"@"+revision)
			if err == nil && ok && cached.CheckRevision == revision && cached.Provider == provider && cached.IP == exit && cached.Family == exitFamily && cached.Status == model.Pass && cached.ExpiresAt.After(time.Now()) {
				results = append(results, cached)
				fresh = append(fresh, false)
				continue
			}
		}
		usedTemplate := strings.Contains(address, "{ip}")
		if usedTemplate {
			if exit == "" {
				results = append(results, result)
				fresh = append(fresh, false)
				continue
			}
			address = strings.ReplaceAll(address, "{ip}", url.QueryEscape(exit))
		}
		result.ExpiresAt = result.MeasuredAt.Add(7 * 24 * time.Hour)
		isFreshPass := false
		func() {
			ctx, cancel := context.WithTimeout(ctx, plan.ProbeTimeout)
			defer cancel()
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
			if err != nil {
				return
			}
			response, err := client.Do(request)
			if err != nil {
				return
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusOK {
				return
			}
			var data struct {
				IP          string   `json:"ip"`
				Query       string   `json:"query"`
				Country     string   `json:"country"`
				CountryCode string   `json:"country_code"`
				ASN         string   `json:"asn"`
				Org         string   `json:"org"`
				AS          string   `json:"as"`
				NetworkType string   `json:"network_type"`
				Reputation  *float64 `json:"reputation"`
				Confidence  float64  `json:"confidence"`
			}
			if err = json.NewDecoder(io.LimitReader(response.Body, 16<<10)).Decode(&data); err != nil {
				return
			}
			if data.IP == "" {
				data.IP = data.Query
			}
			if data.IP == "" {
				data.IP = exit
			}
			ip, err := netip.ParseAddr(data.IP)
			if err != nil || !ip.IsGlobalUnicast() || ip.IsPrivate() {
				return
			}
			result.IP = ip.String()
			result.Family = 6
			if ip.Is4() {
				result.Family = 4
			}
			result.Country = data.CountryCode
			if result.Country == "" {
				result.Country = data.Country
			}
			result.ASN = data.ASN
			if result.ASN == "" {
				result.ASN = data.AS
			}
			if result.ASN == "" {
				result.ASN = data.Org
			}
			result.NetworkType = data.NetworkType
			if len(result.Country) != 2 {
				result.Country = ""
			}
			if len(result.ASN) > 128 {
				result.ASN = result.ASN[:128]
			}
			if len(result.NetworkType) > 64 {
				result.NetworkType = result.NetworkType[:64]
			}
			if data.Reputation != nil && *data.Reputation >= 0 && *data.Reputation <= 100 && data.Confidence >= 0 && data.Confidence <= 1 {
				result.Reputation = data.Reputation
				result.Confidence = data.Confidence
			}
			if exit != "" && (usedTemplate || result.Family == exitFamily) && result.IP != exit {
				return
			}
			result.Status = model.Pass
			isFreshPass = true
		}()
		results = append(results, result)
		fresh = append(fresh, isFreshPass)
	}
	// Providers queried through the same path must agree on the observed exit per address family.
	observedByFamily := make(map[int]string, 2)
	conflict := false
	for _, result := range results {
		if result.Status == model.Pass {
			if prev, ok := observedByFamily[result.Family]; !ok {
				observedByFamily[result.Family] = result.IP
			} else if result.IP != prev {
				conflict = true
				break
			}
		}
	}
	if conflict {
		for i := range results {
			results[i].Status = model.Unknown
			results[i].Conflict = true
			results[i].Confidence = 0
			results[i].Reputation = nil
		}
		return results
	}
	if plan.SaveIP != nil {
		for i := range results {
			if fresh[i] && results[i].Status == model.Pass {
				if err := plan.SaveIP(ctx, results[i]); err != nil {
					results[i].Status = model.Unknown
				}
			}
		}
	}
	return results
}
