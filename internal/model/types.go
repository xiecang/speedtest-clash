// SPDX-License-Identifier: GPL-3.0-only
package model

import (
	"context"
	"net"
)

const SchemaVersion = 2

const MetricVersion = 2

const MihomoVersion = "1.19.32"

const SingBoxVersion = "1.14.2"

type Core string

const (
	Mihomo  Core = "mihomo"
	SingBox Core = "sing-box"
)

type Format string

const (
	ClashYAML   Format = "clash-yaml"
	SingBoxJSON Format = "sing-box-json"
	URI         Format = "uri"
)

// Node stores a source configuration once, including unrecognized fields.
// Dependencies identify the complete path, rather than independently selectable nodes.
type Node struct {
	Key          string         `json:"node_key"`
	Revision     string         `json:"config_revision"`
	Name         string         `json:"name"`
	Hidden       bool           `json:"hidden,omitempty"`
	Format       Format         `json:"format"`
	Config       map[string]any `json:"config"`
	Dependencies []string       `json:"dependencies,omitempty"`
}

type Diagnostic struct {
	Node    string `json:"node,omitempty"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type DecodeResult struct {
	SchemaVersion int               `json:"schema_version"`
	Nodes         []Node            `json:"nodes"`
	Diagnostics   []Diagnostic      `json:"diagnostics,omitempty"`
	Providers     map[string]string `json:"providers,omitempty"`
}

type DialContext func(context.Context, string, string) (net.Conn, error)
