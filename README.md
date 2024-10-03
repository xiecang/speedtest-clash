# SpeedTest-Clash

基于 Clash 核心的测速工具，支持测试是否支持解锁 ChatGPT 的 Web、Android、IOS 端。

## Usages

```bash
➜ speedtest-clash
Usage of speedtest-clash:
  -c string
    	configuration file path, also support http(s) url
  -concurrent int
    	download concurrent size (default 4)
  -f string
    	filter proxies by name, use regexp (default ".*")
  -l string
    	liveness object, support http(s) url, support payload too (default "https://speed.cloudflare.com/__down?bytes=%d")
  -output string
    	output result to csv/yaml file
  -size int
    	download size for testing proxies (default 104857600)
  -sort string
    	sort field for testing proxies, b for bandwidth, t for TTFB (default "b")
  -timeout duration
    	timeout for testing proxies (default 5s)
```


```golang
import (
    "github.com/xiecang/speedtest-clash/speedtest"
)

var t, err = speedtest.NewTest(speedtest.Options{
    DownloadSize:     1024 * 1024 * 10,
    TestGPT:          true,
    ConfigPath:       configStr,
    SortField:        speedtest.SortFieldTTFB,
    Timeout:          30 * time.Second,
    Concurrent:       5,
    URLForTest:       []string{"https://www.google.com"},
})

if err != nil {
    panic(err)
}

_, err = t.TestSpeed()
if err != nil {
    panic(err)
}
proxies, err := t.AliveProxiesWithResult()
```

## License

[MIT](LICENSE)
