package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"syscall"
	"time"

	nq "github.com/xiecang/node-quality/v2"
	"github.com/xiecang/node-quality/v2/codec"
	"github.com/xiecang/node-quality/v2/engine"
	"github.com/xiecang/node-quality/v2/filter"
	"github.com/xiecang/node-quality/v2/probe"
)

const usageLine = "usage: node-quality probe-server|evaluate|convert|capabilities"

func main() {
	if err := run(os.Args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type aiCandidate struct {
	report nq.Report
	graph  []codec.Node
}

func chunkAICandidates(candidates []aiCandidate, maxNodes int) (batches [][]codec.Node, targets [][]string, evidence []map[string]nq.Report) {
	var curNodes []codec.Node
	curSeen := map[string]bool{}
	var curTargets []string
	curEvidence := map[string]nq.Report{}

	flush := func() {
		if len(curNodes) == 0 {
			return
		}
		batches = append(batches, curNodes)
		targets = append(targets, curTargets)
		evidence = append(evidence, curEvidence)
		curNodes = nil
		curSeen = map[string]bool{}
		curTargets = nil
		curEvidence = map[string]nq.Report{}
	}

	for _, c := range candidates {
		added := 0
		for _, n := range c.graph {
			if !curSeen[n.Key] {
				added++
			}
		}
		if len(curNodes) > 0 && len(curNodes)+added > maxNodes {
			flush()
		}
		curTargets = append(curTargets, c.report.NodeKey)
		curEvidence[c.report.NodeKey] = c.report
		for _, n := range c.graph {
			if !curSeen[n.Key] {
				curSeen[n.Key] = true
				curNodes = append(curNodes, n)
			}
		}
	}
	flush()
	return batches, targets, evidence
}

func run(args []string) error {
	if len(args) < 2 {
		return errors.New(usageLine)
	}
	cmd := args[1]
	switch cmd {
	case "-h", "--help", "help":
		fmt.Fprintln(os.Stdout, usageLine)
		return nil
	case "probe-server", "evaluate", "convert", "capabilities":
	default:
		return fmt.Errorf("unknown command %q (%s)", cmd, usageLine)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	flags := flag.NewFlagSet(cmd, flag.ContinueOnError)
	address := flags.String("listen", "127.0.0.1:8070", "probe listen address")
	source := flags.String("source", "", "local source file or subscription URL")
	format := flags.String("format", "clash-yaml", "conversion target (clash-yaml, sing-box-json, uri)")
	filterExpr := flags.String("filter", "", "node filter expression (expr syntax)")
	include := flags.String("name-regex-contain", "", "RE2 name regex to include")
	exclude := flags.String("name-regex-not-contain", "", "RE2 name regex to exclude")
	core := flags.String("core", "mihomo", "native evaluation core (mihomo or sing-box)")
	probeURL := flags.String("probe-url", "", "HTTPS probe base URL")
	profile := flags.String("profile", "general", "general, ai, availability, or services")
	concurrency := flags.Int("ai-concurrency", 5, "concurrent AI nodes")
	bandwidth := flags.Int64("max-bytes-per-second", 0, "shared download rate budget")
	cert := flags.String("tls-cert", "", "TLS certificate file")
	key := flags.String("tls-key", "", "TLS private key file")
	if err := flags.Parse(args[2:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	plan := nq.DefaultPlan()
	plan.Core = engine.Core(*core)
	plan.ProbeURL = *probeURL
	plan.Profile = *profile
	plan.AI.Concurrency = *concurrency
	plan.ProbeToken = os.Getenv("NODE_QUALITY_TOKEN")
	plan.Filter, plan.NameRegexContain, plan.NameRegexNotContain = *filterExpr, *include, *exclude

	switch cmd {
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
		server := &http.Server{
			BaseContext:       func(net.Listener) context.Context { return ctx },
			Addr:              *address,
			Handler:           nq.ServerHandler(plan, nq.NewBudget(*bandwidth), probe.Options{Token: plan.ProbeToken}),
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       120 * time.Second,
		}
		go func() {
			<-ctx.Done()
			shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = server.Shutdown(shutdown)
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
		return json.NewEncoder(os.Stdout).Encode(engine.Capabilities())

	case "convert":
		selector, err := filter.Compile(filter.Options{
			Filter:              *filterExpr,
			NameRegexContain:    *include,
			NameRegexNotContain: *exclude,
		})
		if err != nil {
			return err
		}
		var all []codec.Node
		seen := map[string]bool{}
		if err := codec.LoadSource(ctx, *source, &http.Client{Timeout: time.Minute}, func(batch []codec.Node) error {
			for _, n := range batch {
				if !seen[n.Key] {
					seen[n.Key] = true
					all = append(all, n)
				}
			}
			return nil
		}); err != nil {
			return err
		}
		selected, err := selector.Prune(all)
		if err != nil {
			return err
		}
		output, err := codec.Encode(selected, codec.Format(*format))
		if err != nil {
			return err
		}
		_, err = os.Stdout.Write(output)
		return err

	case "evaluate":
		prepared, err := nq.PreparePlan(plan)
		if err != nil {
			return err
		}
		plan = prepared
		budget := nq.NewBudget(*bandwidth)
		var candidates []aiCandidate
		candidateSeen := map[string]bool{}
		requested := plan.Profile
		var collectedReports []nq.Report

		err = codec.LoadSource(ctx, *source, &http.Client{Timeout: time.Minute}, func(nodes []codec.Node) error {
			stagePlan := plan
			if requested == "ai" {
				stagePlan.Profile = "general"
			}
			reports, err := nq.Evaluate(ctx, nodes, stagePlan, budget)
			if err != nil {
				return err
			}
			if requested != "ai" {
				collectedReports = append(collectedReports, reports...)
				return nil
			}
			byKey := make(map[string]codec.Node, len(nodes))
			for _, n := range nodes {
				byKey[n.Key] = n
			}
			for _, r := range reports {
				if r.Status != nq.Pass || candidateSeen[r.NodeKey] {
					continue
				}
				candidateSeen[r.NodeKey] = true
				seen := map[string]bool{}
				var graph []codec.Node
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
				candidates = append(candidates, aiCandidate{report: r, graph: graph})
			}
			sort.Slice(candidates, func(i, j int) bool {
				a, b := candidates[i].report, candidates[j].report
				if a.Metrics.ColdHealthMedian != b.Metrics.ColdHealthMedian {
					return a.Metrics.ColdHealthMedian < b.Metrics.ColdHealthMedian
				}
				return a.NodeKey < b.NodeKey
			})
			if len(candidates) > plan.AI.CandidateLimit {
				for _, dropped := range candidates[plan.AI.CandidateLimit:] {
					delete(candidateSeen, dropped.report.NodeKey)
				}
				candidates = candidates[:plan.AI.CandidateLimit]
			}
			return nil
		})
		if err != nil {
			return err
		}
		if requested == "ai" {
			batches, targets, evidence := chunkAICandidates(candidates, 200)
			collectedReports = make([]nq.Report, 0, len(candidates))
			for i := range batches {
				aiPlan := plan
				aiPlan.TargetKeys = targets[i]
				aiPlan.GeneralEvidence = evidence[i]
				part, err := nq.Evaluate(ctx, batches[i], aiPlan, budget)
				if err != nil {
					return err
				}
				collectedReports = append(collectedReports, part...)
			}
		}
		if collectedReports == nil {
			collectedReports = []nq.Report{}
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]any{
			"schema_version": codec.SchemaVersion,
			"reports":        collectedReports,
		})

	default:
		return errors.New(usageLine)
	}
}
