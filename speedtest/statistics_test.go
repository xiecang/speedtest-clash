package speedtest

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/xiecang/speedtest-clash/speedtest/models"
)

func TestLogSummary(t *testing.T) {
	tester := &Test{
		aliveProxies: []models.CProxyWithResult{
			{
				Result: models.Result{
					Name:      "Node1",
					Bandwidth: 10 * 1024 * 1024,
					Delay:     100,
					Country:   "CN",
				},
			},
			{
				Result: models.Result{
					Name:      "Node2",
					Bandwidth: 20 * 1024 * 1024,
					Delay:     200,
					Country:   "US",
				},
			},
			{
				Result: models.Result{
					Name:      "Node3",
					Bandwidth: 15 * 1024 * 1024,
					Delay:     150,
					Country:   "CN",
				},
			},
		},
		totalCount:   new(int32),
		count:        new(int32),
		invalidCount: new(int32),
		aliveCount:   new(int32),
	}
	*tester.totalCount = 3
	*tester.count = 3
	*tester.aliveCount = 3

	// This just ensures it doesn't panic and prints something
	// In a real scenario we could capture stdout
	tester.LogNum()
}

func TestStopProgressRobustness(t *testing.T) {
	tester, _ := NewTest(models.Options{
		ConfigPath: "dummy.yaml",
		Progress: models.ProgressConfig{
			PrintProgress: true,
		},
	})

	tester.startAutoProgress()
	time.Sleep(100 * time.Millisecond)

	// Double stop should not panic
	tester.stopProgress()
	assert.NotPanics(t, func() {
		tester.stopProgress()
	})
}
