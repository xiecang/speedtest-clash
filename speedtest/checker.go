package speedtest

import (
	"context"
	"log/slog"
	"sync"

	C "github.com/metacubex/mihomo/constant"
	"github.com/xiecang/speedtest-clash/speedtest/check"
	"github.com/xiecang/speedtest-clash/speedtest/models"
)

var (
	mp = map[models.CheckType]models.Checker{
		models.CheckTypeGPTWeb:     check.NewGPTWebChecker(),
		models.CheckTypeGPTAndroid: check.NewGPTAndroidChecker(),
		models.CheckTypeGPTIOS:     check.NewGPTIOSChecker(),
		models.CheckTypeDisney:     check.NewDisneyChecker(),
		models.CheckTypeNetflix:    check.NewNetflixChecker(),
		models.CheckTypeGemini:     check.NewGeminiChecker(),
		models.CheckTypeCountry:    check.NewCountryChecker(),
	}
)

func checkProxy(ctx context.Context, proxy C.Proxy, types []models.CheckType, logger *slog.Logger) []models.CheckResult {
	var (
		res []models.CheckResult
		ch  = make(chan models.CheckResult, len(types))
		wg  sync.WaitGroup
	)
	logger = resolveLogger(logger)
	for _, checkType := range types {
		if f, exist := mp[checkType]; exist {
			wg.Add(1)
			go func(ctx context.Context, checkType models.CheckType, f models.Checker, proxy C.Proxy) {
				defer wg.Done()
				r, err := f.Check(ctx, proxy)
				if err != nil {
					logger.Info("proxy check failed",
						slog.String("proxy_name", proxy.Name()),
						slog.String("proxy_addr", proxy.Addr()),
						slog.String("check_type", string(checkType)),
						slog.Any("error", err),
					)
				}
				ch <- r
			}(ctx, checkType, f, proxy)
		} else {
			logger.Error("not supported checkType", slog.String("check_type", string(checkType)))
		}
	}
	wg.Wait()
	close(ch)
	for r := range ch {
		res = append(res, r)
	}
	return res
}
