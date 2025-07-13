package speedtest

import (
	"context"
	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/log"
	"github.com/xiecang/speedtest-clash/speedtest/check"
	"github.com/xiecang/speedtest-clash/speedtest/models"
	"sync"
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

func checkProxy(ctx context.Context, proxy C.Proxy, types []models.CheckType) []models.CheckResult {
	var (
		res []models.CheckResult
		ch  = make(chan models.CheckResult, len(types))
		wg  sync.WaitGroup
	)
	for _, checkType := range types {
		if f, exist := mp[checkType]; exist {
			wg.Add(1)
			go func(ctx context.Context, checkType models.CheckType, f models.Checker, proxy C.Proxy) {
				defer wg.Done()
				r, err := f.Check(ctx, proxy)
				if err != nil {
					log.Infoln("[%s](%s) check %s failed, err: %s", proxy.Name(), proxy.Addr(), checkType, err)
				}
				ch <- r
			}(ctx, checkType, f, proxy)
		} else {
			log.Errorln("not supported checkType: %s", checkType)
		}
	}
	wg.Wait()
	close(ch)
	for r := range ch {
		res = append(res, r)
	}
	return res
}
