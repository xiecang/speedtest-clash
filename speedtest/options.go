package speedtest

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/xiecang/speedtest-clash/speedtest/models"
)

func resolveLogger(l *slog.Logger) *slog.Logger {
	if l != nil {
		return l
	}
	return slog.Default()
}

func loggerFromOptions(options *models.Options) *slog.Logger {
	if options == nil {
		return slog.Default()
	}
	options.Logger = resolveLogger(options.Logger)
	return options.Logger
}

func debugf(options *models.Options, format string, args ...any) {
	loggerFromOptions(options).Debug(fmt.Sprintf(format, args...))
}

func infof(options *models.Options, format string, args ...any) {
	loggerFromOptions(options).Info(fmt.Sprintf(format, args...))
}

func warnf(options *models.Options, format string, args ...any) {
	loggerFromOptions(options).Warn(fmt.Sprintf(format, args...))
}

func errorf(options *models.Options, format string, args ...any) {
	loggerFromOptions(options).Error(fmt.Sprintf(format, args...))
}

func normalizeOptions(options *models.Options) (bool, string) {
	if options.ConfigPath == "" && len(options.Proxies) == 0 && !options.KeepOpen {
		return false, "配置不能为空，请至少提供 ConfigPath 或 Proxies"
	}
	options.Logger = resolveLogger(options.Logger)
	if options.DownloadSize == 0 {
		options.DownloadSize = 100 * 1024 * 1024
	}
	if options.Timeout == 0 {
		options.Timeout = 2 * time.Minute
	}
	if options.ProbeTimeout == 0 {
		options.ProbeTimeout = 5 * time.Second
		if options.Timeout > 0 && options.Timeout < options.ProbeTimeout {
			options.ProbeTimeout = options.Timeout
		}
	}
	if options.SortField == "" {
		options.SortField = models.SortFieldBandwidth
	}
	if options.LivenessAddr == "" {
		options.LivenessAddr = models.DefaultLivenessAddr
	}
	if options.Concurrent == 0 {
		options.Concurrent = cpuCount
	}
	if options.DelayTestUrl == "" {
		options.DelayTestUrl = "https://i.ytimg.com/generate_204"
	}
	if options.Progress.ProgressInterval <= 0 {
		options.Progress.ProgressInterval = 3 * time.Second
	}

	return true, ""
}
