package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	nq "github.com/xiecang/node-quality/v2"
	"github.com/xiecang/node-quality/v2/probe"
)

func TestCLIEvaluateEmitsSingleValidJSONDocumentAcrossBatches(t *testing.T) {
	probeSrv := httptest.NewServer(probe.Handler(probe.Options{FrameInterval: 10}))
	defer probeSrv.Close()

	var yamlSrc strings.Builder
	yamlSrc.WriteString("proxies:\n")
	for i := 0; i < 205; i++ {
		fmt.Fprintf(&yamlSrc, "- {name: d%d, type: direct, udp: %t, tfo: %t, x-seq: %d}\n", i, i%2 == 0, i%3 == 0, i)
	}
	srcFile := t.TempDir() + "/nodes.yaml"
	if err := os.WriteFile(srcFile, []byte(yamlSrc.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	var raw []byte
	var readErr error
	done := make(chan struct{})
	go func() {
		raw, readErr = io.ReadAll(r)
		close(done)
	}()
	os.Stdout = w
	runErr := run([]string{"node-quality", "evaluate", "-source", srcFile, "-probe-url", probeSrv.URL, "-profile", "availability"})
	_ = w.Close()
	os.Stdout = oldStdout
	<-done
	if runErr != nil {
		t.Fatalf("CLI evaluate failed: %v", runErr)
	}
	if readErr != nil {
		t.Fatal(readErr)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	var doc struct {
		SchemaVersion int         `json:"schema_version"`
		Reports       []nq.Report `json:"reports"`
	}
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("expected single valid JSON document, got decode err=%v raw=%s", err, raw)
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		t.Fatalf("CLI emitted trailing multi-object JSON on stdout: %v", err)
	}
	if doc.SchemaVersion != 2 || len(doc.Reports) != 205 {
		t.Fatalf("unexpected CLI output: schema=%d len=%d", doc.SchemaVersion, len(doc.Reports))
	}
}
