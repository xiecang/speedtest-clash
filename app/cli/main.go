package main

import (
	"flag"
	"github.com/phuslu/log"
	"github.com/xiecang/speedtest-clash/speedtest"
	"github.com/xiecang/speedtest-clash/speedtest/models"
	"strings"
	"time"
)

var (
	livenessObject     = flag.String("l", "https://speed.cloudflare.com/__down?bytes=%d", "liveness object, support http(s) url, support payload too")
	configPathConfig   = flag.String("c", "", "configuration file path, also support http(s) url")
	filterRegexConfig  = flag.String("f", ".*", "filter proxies by name, use regexp")
	downloadSizeConfig = flag.Int("size", 1024*1024*100, "download size for testing proxies")
	timeoutConfig      = flag.Duration("timeout", time.Second*5, "timeout for testing proxies")
	sortField          = flag.String("sort", "b", "sort field for testing proxies, b for bandwidth, t for TTFB")
	output             = flag.String("output", "", "output result to csv/yaml file")
	concurrent         = flag.Int("concurrent", 4, "download concurrent size")
)

func main() {
	flag.Parse()

	if *configPathConfig == "" {
		log.Fatal().Msgf("Please specify the configuration file")
	}

	var options = models.Options{
		LivenessAddr:     *livenessObject,
		DownloadSize:     *downloadSizeConfig,
		Timeout:          *timeoutConfig,
		ConfigPath:       *configPathConfig,
		NameRegexContain: *filterRegexConfig,
		SortField:        models.SortField(*sortField),
		Concurrent:       *concurrent,
	}

	var t, err = speedtest.NewTest(options)
	if err != nil {
		log.Fatal().Msgf("%v", err)
	}
	_, err = t.TestSpeed()
	if err != nil {
		log.Fatal().Msgf("%v", err)
	}

	d, err := t.AliveProxiesToJson()
	log.Info().Msgf("json: %s", d)
	t.LogAlive()

	if strings.EqualFold(*output, "yaml") {
		if err = t.WriteToYaml(); err != nil {
			log.Fatal().Msgf("Failed to write yaml: %s", err)
		}
	} else if strings.EqualFold(*output, "csv") {
		if err := t.WriteToCsv(); err != nil {
			log.Fatal().Msgf("Failed to write csv: %s", err)
		}
	}
}
