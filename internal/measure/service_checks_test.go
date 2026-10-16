// SPDX-License-Identifier: GPL-3.0-only
package measure

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xiecang/node-quality/v2/internal/model"
	"github.com/xiecang/node-quality/v2/internal/probe"
)

func TestOptionalMobileServiceResponseAndRevision(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   model.Status
	}{{403, `{"cf_details":"Request is not allowed. Please try again later."}`, model.Pass}, {200, "hello", model.Unknown}, {403, "You may be connected to a disallowed ISP", model.Fail}, {200, "verify you are human", model.Unknown}, {200, "unsupported_country", model.Fail}} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		}))
		check := model.ServiceCheck{Kind: "openai-mobile", URL: server.URL}
		got := checkOptionalService(context.Background(), server.Client(), check, time.Second)
		server.Close()
		if got.Status != tc.want || got.CheckRevision != ServiceCheckRevision(check) {
			t.Fatal(got, tc)
		}
	}
	p := model.DefaultPlan()
	before := PlanRevision(p)
	p.ServiceChecks = map[string]model.ServiceCheck{"OpenAI-Android": {Kind: "openai-mobile", URL: "https://android.chat.openai.com"}}
	if PlanRevision(p) != before {
		t.Fatal("optional service invalidated core evidence")
	}
}

func TestLightProfilesAvoidDownloadsAndReuseOnlyMatchingService(t *testing.T) {
	for _, core := range []model.Core{model.Mihomo, model.SingBox} {
		t.Run(string(core), func(t *testing.T) {
			var downloads, services atomic.Int64
			base := probe.ProbeHandler(probe.ProbeOptions{})
			p, n, server := auditSetup(t, core, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/download") {
					downloads.Add(1)
				}
				if r.URL.Path == "/service" {
					services.Add(1)
					_, _ = w.Write([]byte("OK"))
					return
				}
				base.ServeHTTP(w, r)
			}))
			p.Profile = "availability"
			reports, err := Evaluate(context.Background(), []model.Node{n}, p, NewBudget(0))
			if err != nil || len(reports) != 1 || reports[0].Status != model.Pass || reports[0].GeneralStatus == model.Pass || len(reports[0].IP) > 0 || len(reports[0].Services) > 0 || downloads.Load() != 0 {
				t.Fatal(reports, err)
			}
			p.Profile = "services"
			p.ServiceChecks = map[string]model.ServiceCheck{"test": {Kind: "http", URL: server.URL + "/service"}}
			for i := 0; i < 3; i++ {
				reports, err = Evaluate(context.Background(), []model.Node{n}, p, NewBudget(0))
				if err != nil || reports[0].Services["test"].Status != model.Pass || downloads.Load() != 0 {
					t.Fatal(reports, err)
				}
				p.ServiceEvidence = reports[0].Services
			}
			if services.Load() != 1 {
				t.Fatal("fresh service cache was ignored", services.Load())
			}
			p.ServiceChecks["test"] = model.ServiceCheck{Kind: "http", URL: server.URL + "/service?v=2"}
			if _, err = Evaluate(context.Background(), []model.Node{n}, p, NewBudget(0)); err != nil || services.Load() != 2 {
				t.Fatal("changed definition reused", services.Load(), err)
			}
		})
	}
}
