// SPDX-License-Identifier: GPL-3.0-only
package model

type StreamFrame struct {
	Kind             string `json:"kind"`
	Sequence         int64  `json:"sequence"`
	IdleMilliseconds int64  `json:"idle_ms,omitempty"`
}
