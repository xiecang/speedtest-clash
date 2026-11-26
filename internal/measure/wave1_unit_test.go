// SPDX-License-Identifier: GPL-3.0-only
package measure

import (
	"context"
	"errors"
	"strings"
	"syscall"
	"testing"

	"github.com/xiecang/node-quality/v2/internal/codec"
	"github.com/xiecang/node-quality/v2/internal/filter"
	"github.com/xiecang/node-quality/v2/internal/model"
)

func TestFailureCodesAndEnvironmentErrnos(t *testing.T) {
	for _, tc := range []struct {
		err        error
		wantOrigin model.FailureOrigin
		wantCode   string
	}{
		{syscall.EADDRNOTAVAIL, model.FailureOriginEnvironment, "environment_resource_exhausted"},
		{syscall.ENOBUFS, model.FailureOriginEnvironment, "environment_resource_exhausted"},
		{syscall.EHOSTUNREACH, model.FailureOriginEnvironment, "environment_route_unreachable"},
		{syscall.ENETDOWN, model.FailureOriginEnvironment, "environment_route_unreachable"},
		{errNodeAuthRequired, model.FailureOriginNode, "node_auth_required"},
		{errors.New("HTTP need auth"), model.FailureOriginNode, "node_auth_required"},
		{errors.New("authentication required"), model.FailureOriginNode, "node_auth_required"},
	} {
		origin, code := failureOrigin(tc.err, context.Background())
		if origin != tc.wantOrigin || code != tc.wantCode {
			t.Fatalf("err=%v got (%s,%s) want (%s,%s)", tc.err, origin, code, tc.wantOrigin, tc.wantCode)
		}
	}

	if got := capabilityFailureCode(errors.New("outbound capability is disabled in a measurement runtime")); got != "unsupported_core" {
		t.Fatalf("got %q want unsupported_core", got)
	}

	p := model.DefaultPlan()
	p.ProbeURL = "https://probe.example"
	p.Profile = "bogus"
	err := ValidatePlan(p)
	if err == nil {
		t.Fatal("expected invalid profile error")
	}
	for _, prof := range []string{"general", "ai", "availability", "services"} {
		if !strings.Contains(err.Error(), prof) {
			t.Fatalf("profile error %q missing %q", err.Error(), prof)
		}
	}
}

func TestEvaluateRecompilesMutatedPreparedFilterAndSetsCapabilityGeneralStatus(t *testing.T) {
	ctx := context.Background()
	sel, err := filter.Compile(ctx, filter.Options{})
	if err != nil {
		t.Fatal(err)
	}
	n1, err := codec.NewNode(map[string]any{"name": "alpha", "type": "tailscale", "server": "192.0.2.1", "port": 443}, model.ClashYAML)
	if err != nil {
		t.Fatal(err)
	}
	n2, err := codec.NewNode(map[string]any{"name": "beta", "type": "tailscale", "server": "192.0.2.2", "port": 443}, model.ClashYAML)
	if err != nil {
		t.Fatal(err)
	}

	p := model.DefaultPlan()
	p.ProbeURL = "https://probe.example"
	p.Profile = "services"
	p.ServiceChecks = nil
	p.TargetSelector = sel
	// Mutate Filter after TargetSelector was prepared with empty filter.
	p.Filter = "name=beta"

	reports, err := Evaluate(ctx, []model.Node{n1, n2}, p, NewBudget(0))
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 1 || reports[0].NodeKey != n2.Key {
		t.Fatalf("expected mutated filter to select only beta, got %+v", reports)
	}
	if reports[0].Status != model.Unknown || reports[0].GeneralStatus != model.Unknown || reports[0].FailureCode != "unsupported_core" {
		t.Fatalf("unexpected capability failure report: %+v", reports[0])
	}
}
