// SPDX-License-Identifier: GPL-3.0-only
package integration

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	nq "github.com/xiecang/node-quality/v2"
)

func TestNativeSingBoxDirectAndClose(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("native")) }))
	defer server.Close()
	node, err := nq.NewNode(map[string]any{"type": "direct", "tag": "test"}, nq.SingBoxJSON)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := nq.OpenBatch(context.Background(), []nq.Node{node}, nq.SingBox)
	if err != nil {
		t.Fatal(err)
	}
	defer batch.Close()
	dial, err := batch.Dialer(node.Key)
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{DialContext: dial}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	content, err := io.ReadAll(response.Body)
	if err != nil || string(content) != "native" {
		t.Fatalf("content=%q error=%v", content, err)
	}
	if err = batch.Close(); err != nil {
		t.Fatal(err)
	}
	if err = batch.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestDependencyCycleRejected(t *testing.T) {
	nodes := []nq.Node{{Key: "a", Dependencies: []string{"b"}}, {Key: "b", Dependencies: []string{"a"}}}
	if _, err := nq.OpenBatch(context.Background(), nodes, nq.SingBox); err == nil {
		t.Fatal("cycle accepted")
	}
}
