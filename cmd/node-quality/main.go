// SPDX-License-Identifier: GPL-3.0-only
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"syscall"
	"time"

	nq "github.com/xiecang/node-quality/v2"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 2 {
		return errors.New("usage: node-quality probe-server|evaluate|convert")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	flags := flag.NewFlagSet(os.Args[1], flag.ContinueOnError)
	address := flags.String("listen", "127.0.0.1:8070", "probe listen address")
	source := flags.String("source", "", "local source file or subscription URL")
	format := flags.String("format", "clash-yaml", "conversion target")
	filter := flags.String("filter", "", "cvt node filter expression")
	include := flags.String("name-regex-contain", "", "legacy Go name regex to include")
	exclude := flags.String("name-regex-not-contain", "", "legacy Go name regex to exclude")
	core := flags.String("core", "mihomo", "native evaluation core")
	probe := flags.String("probe-url", "", "HTTPS probe base URL")
	profile := flags.String("profile", "general", "general or ai")
	concurrency := flags.Int("ai-concurrency", 5, "concurrent AI nodes")
	bandwidth := flags.Int64("max-bytes-per-second", 0, "shared download rate budget")
	cert := flags.String("tls-cert", "", "TLS certificate file")
	key := flags.String("tls-key", "", "TLS private key file")
	if err := flags.Parse(os.Args[2:]); err != nil {
		return err
	}
	plan := nq.DefaultPlan()
	plan.Core = nq.Core(*core)
	plan.ProbeURL = *probe
	plan.Profile = *profile
	plan.AI.Concurrency = *concurrency
	plan.ProbeToken = os.Getenv("NODE_QUALITY_TOKEN")
	plan.Filter, plan.NameRegexContain, plan.NameRegexNotContain = *filter, *include, *exclude
	switch os.Args[1] {
	case "probe-server":
		host, _, err := net.SplitHostPort(*address)
		if err != nil {
			return err
		}
		ip := net.ParseIP(host)
		if (ip == nil || !ip.IsLoopback()) && plan.ProbeToken == "" {
			return errors.New("public probe requires NODE_QUALITY_TOKEN")
		}
		if (*cert == "") != (*key == "") {
			return errors.New("tls-cert and tls-key must be configured together")
		}
		server := &http.Server{BaseContext: func(net.Listener) context.Context { return ctx }, Addr: *address, Handler: nq.Handler(plan, nq.NewBudget(*bandwidth), nq.ProbeOptions{Token: plan.ProbeToken}), ReadHeaderTimeout: 10 * time.Second}
		go func() {
			<-ctx.Done()
			shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			server.Shutdown(shutdown)
		}()
		if *cert != "" && *key != "" {
			err = server.ListenAndServeTLS(*cert, *key)
		} else {
			err = server.ListenAndServe()
		}
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case "capabilities":
		return json.NewEncoder(os.Stdout).Encode(nq.Capabilities())
	case "convert":
		file, err := os.Open(*source)
		if err != nil {
			return err
		}
		defer file.Close()
		content, err := io.ReadAll(io.LimitReader(file, nq.MaxSourceBytes+1))
		if err != nil {
			return err
		}
		output, _, err := nq.ConvertSelected(ctx, content, nq.Format(*format), nq.SelectionOptions{Filter: *filter, NameRegexContain: *include, NameRegexNotContain: *exclude})
		if err != nil {
			return err
		}
		_, err = os.Stdout.Write(output)
		return err
	case "evaluate":
		prepared, err := nq.PreparePlan(ctx, plan)
		if err != nil {
			return err
		}
		plan = prepared
		budget := nq.NewBudget(*bandwidth)
		type selected struct {
			report nq.Report
			graph  []nq.Node
		}
		var candidates []selected
		requested := plan.Profile
		err = nq.LoadSource(ctx, *source, &http.Client{Timeout: time.Minute}, func(nodes []nq.Node) error {
			general := plan
			general.Profile = "general"
			if requested != "ai" {
				general = plan
			}
			reports, err := nq.Evaluate(ctx, nodes, general, budget)
			if err != nil {
				return err
			}
			if err = json.NewEncoder(os.Stdout).Encode(map[string]any{"schema_version": 2, "reports": reports}); err != nil {
				return err
			}
			if requested != "ai" {
				return nil
			}
			byKey := make(map[string]nq.Node)
			for _, n := range nodes {
				byKey[n.Key] = n
			}
			for _, r := range reports {
				if r.Status != nq.Pass {
					continue
				}
				seen := map[string]bool{}
				var graph []nq.Node
				var visit func(string)
				visit = func(key string) {
					if seen[key] {
						return
					}
					seen[key] = true
					n := byKey[key]
					for _, dep := range n.Dependencies {
						visit(dep)
					}
					graph = append(graph, n)
				}
				visit(r.NodeKey)
				candidates = append(candidates, selected{r, graph})
			}
			sort.Slice(candidates, func(i, j int) bool {
				a, b := candidates[i].report, candidates[j].report
				if a.Metrics.ColdHealthMedian != b.Metrics.ColdHealthMedian {
					return a.Metrics.ColdHealthMedian < b.Metrics.ColdHealthMedian
				}
				return a.NodeKey < b.NodeKey
			})
			if len(candidates) > plan.AI.CandidateLimit {
				candidates = candidates[:plan.AI.CandidateLimit]
			}
			return nil
		})
		if err != nil || requested != "ai" {
			return err
		}
		var graph []nq.Node
		seen := map[string]bool{}
		for _, c := range candidates {
			plan.TargetKeys = append(plan.TargetKeys, c.report.NodeKey)
			for _, n := range c.graph {
				if !seen[n.Key] {
					seen[n.Key] = true
					graph = append(graph, n)
				}
			}
		}
		if len(graph) == 0 {
			return nil
		}
		reports, err := nq.Evaluate(ctx, graph, plan, budget)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"schema_version": 2, "reports": reports})

	default:
		return errors.New("unknown command")
	}
}
