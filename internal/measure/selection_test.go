// SPDX-License-Identifier: GPL-3.0-only
package measure

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/xiecang/node-quality/v2/internal/codec"
	"github.com/xiecang/node-quality/v2/internal/model"
	"github.com/xiecang/node-quality/v2/internal/probe"
)

func TestSelectionOnlyMeasuresRootsAndPreservesRevision(t *testing.T) {
	var calls atomic.Int64
	handler := probe.ProbeHandler(probe.ProbeOptions{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); handler.ServeHTTP(w, r) }))
	defer server.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstream, err := net.Dial("tcp", r.Host)
		if err != nil {
			http.Error(w, "dial failed", 502)
			return
		}
		defer upstream.Close()
		connection, buffer, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer connection.Close()
		buffer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		buffer.Flush()
		done := make(chan struct{})
		go func() { io.Copy(upstream, buffer); upstream.Close(); close(done) }()
		io.Copy(connection, upstream)
		connection.Close()
		<-done
	}))
	defer proxy.Close()
	address, _ := url.Parse(proxy.URL)
	host, port, _ := net.SplitHostPort(address.Host)
	portNum, _ := strconv.Atoi(port)
	decoded, err := codec.Decode([]byte(fmt.Sprintf(`{"outbounds":[{"type":"http","tag":"entry","server":%q,"server_port":%d},{"type":"http","tag":"exit","server":%q,"server_port":%d,"username":"exit","detour":"entry"}]}`, host, portNum, host, portNum)))
	if err != nil {
		t.Fatal(err)
	}
	plan := model.DefaultPlan()
	plan.Profile = "availability"
	plan.ProbeURL = server.URL
	for _, core := range []model.Core{model.Mihomo, model.SingBox} {
		plan.Core = core
		revision := PlanRevision(plan)
		plan.Filter = "name=exit"
		if PlanRevision(plan) != revision {
			t.Fatal("selection invalidates measurement cache")
		}
		reports, err := Evaluate(context.Background(), decoded.Nodes, plan, NewBudget(0))
		if err != nil || len(reports) != 1 || reports[0].Status != model.Pass {
			t.Fatalf("root or dependency lost: %v %v", reports, err)
		}
		previous := calls.Load()
		plan.Filter = "name=absent"
		reports, err = Evaluate(context.Background(), decoded.Nodes, plan, NewBudget(0))
		if err != nil || len(reports) != 0 || calls.Load() != previous {
			t.Fatalf("excluded target uses network: %v %v", reports, err)
		}
	}
}
