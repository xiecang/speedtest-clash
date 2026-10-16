// SPDX-License-Identifier: GPL-3.0-only
package model

type BuildInfo struct {
	Profiles      []string `json:"profiles"`
	SchemaVersion int      `json:"schema_version"`
	Mihomo        string   `json:"mihomo"`
	SingBox       string   `json:"sing_box"`
	Go            string   `json:"go"`
	OS            string   `json:"os"`
	Arch          string   `json:"arch"`
	Tags          []string `json:"tags"`
}
