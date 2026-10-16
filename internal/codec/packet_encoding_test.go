// SPDX-License-Identifier: GPL-3.0-only
package codec_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	nq "github.com/xiecang/node-quality/v2"
)

func TestPacketAliasesConvertWithoutMutatingOriginal(t *testing.T) {
	for _, kind := range []string{"vmess", "vless"} {
		for _, mode := range []string{"xudp", "packetaddr"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				alias := "xudp"
				if mode == "packetaddr" {
					alias = "packet-addr"
				}
				config := map[string]any{"name": "packet", "type": kind, "server": "example.invalid", "port": 443, "uuid": "00000000-0000-4000-8000-000000000001", "cipher": "auto", alias: true, "packet-encoding": mode}
				if kind == "vless" {
					delete(config, "cipher")
				}
				node, err := nq.NewNode(config, nq.ClashYAML)
				if err != nil {
					t.Fatal(err)
				}
				before, _ := json.Marshal(node)
				data, err := nq.Encode([]nq.Node{node}, nq.SingBoxJSON)
				if err != nil {
					t.Fatal(err)
				}
				if err = nq.CheckSingBox(context.Background(), data); err != nil {
					t.Fatal(err)
				}
				var native struct {
					Outbounds []map[string]any `json:"outbounds"`
				}
				if err = json.Unmarshal(data, &native); err != nil {
					t.Fatal(err)
				}
				if len(native.Outbounds) != 1 || native.Outbounds[0]["packet_encoding"] != mode {
					t.Fatalf("native=%s", data)
				}
				after, _ := json.Marshal(node)
				if !reflect.DeepEqual(before, after) {
					t.Fatal("original node mutated")
				}
			})
		}
	}
}

func TestPacketAliasConflictsAndDisabledFlags(t *testing.T) {
	for _, conflict := range []map[string]any{{"xudp": true, "packet-encoding": "packetaddr"}, {"xudp": true, "packet-addr": true}, {"packet-encoding": "xudp", "packet_encoding": "packetaddr"}, {"xudp": "true"}} {
		config := map[string]any{"type": "vless", "server": "example.invalid", "port": 443, "uuid": "00000000-0000-4000-8000-000000000001"}
		for k, v := range conflict {
			config[k] = v
		}
		node, err := nq.NewNode(config, nq.ClashYAML)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = nq.Encode([]nq.Node{node}, nq.SingBoxJSON); err == nil {
			t.Fatal("conflicting alias accepted")
		}
	}
	node, err := nq.NewNode(map[string]any{"type": "vless", "server": "example.invalid", "port": 443, "uuid": "00000000-0000-4000-8000-000000000001", "xudp": false}, nq.ClashYAML)
	if err != nil {
		t.Fatal(err)
	}
	data, err := nq.Encode([]nq.Node{node}, nq.SingBoxJSON)
	if err != nil {
		t.Fatal(err)
	}
	if err = nq.CheckSingBox(context.Background(), data); err != nil {
		t.Fatal(err)
	}
}
