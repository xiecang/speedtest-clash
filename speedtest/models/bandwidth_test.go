package models

import (
	"testing"
)

func TestResult_FormattedBandwidth(t *testing.T) {
	tests := []struct {
		name      string
		bandwidth float64
		want      string
	}{
		{"N/A", 0, "N/A"},
		{"Negative", -1, "N/A"},
		{"Bytes", 500, "500.00B/s"},
		{"KiloBytes", 1024, "1.00KB/s"},
		{"MegaBytes", 1024 * 1024, "1.00MB/s"},
		{"GigaBytes", 1024 * 1024 * 1024, "1.00GB/s"},
		{"TeraBytes", 1024 * 1024 * 1024 * 1024, "1.00TB/s"},
		{"Mixed", 1.5 * 1024 * 1024, "1.50MB/s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &Result{
				Bandwidth: tt.bandwidth,
			}
			if got := r.FormattedBandwidth(); got != tt.want {
				t.Errorf("Result.FormattedBandwidth() = %v, want %v", got, tt.want)
			}
		})
	}
}
