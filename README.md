# SpeedTest-Clash

🚀 基于 Clash 核心的高性能代理测速工具，支持多平台解锁检测和智能缓存优化。

## ✨ 特性

- **多协议支持**: 支持所有主流代理协议
- **智能测速**: 支持自定义测速参数和测速节点
- **并发优化**: 智能并发控制，高效测速
- **实时进度**: 实时显示测速进度
- **解锁检测**: ChatGPT、Netflix、Disney+、Gemini 等平台解锁检测
- **智能缓存**: 内置缓存机制，避免重复测速
- **结果导出**: 支持 CSV、YAML 格式导出，按带宽或延迟排序

## 🚀 快速开始

### 命令行使用

```bash
# 基本测速
speedtest-clash -c config.yaml

# 指定并发数和超时时间
speedtest-clash -c config.yaml -concurrent 8 -timeout 10s

# 过滤特定节点
speedtest-clash -c config.yaml -f "香港|台湾"

# 按延迟排序并导出结果
speedtest-clash -c config.yaml -sort t -output result.csv

# 使用网络配置文件
speedtest-clash -c "https://example.com/config.yaml"
```

### 完整参数说明

```bash
➜ speedtest-clash -h
Usage of speedtest-clash:
  -c string
        配置文件路径，支持本地文件和 HTTP(S) URL
  -concurrent int
        并发测速数量 (默认: CPU核心数*3)
  -f string
        节点名称过滤，支持正则表达式 (默认: ".*")
  -l string
        测速目标地址，支持自定义 URL (默认: "https://speed.cloudflare.com/__down?bytes=%d")
  -output string
        结果输出文件，支持 csv/yaml 格式
  -size int
        测速下载大小，单位字节 (默认: 100MB)
  -sort string
        排序方式: b=带宽, t=延迟 (默认: "b")
  -timeout duration
        单个节点测速超时时间 (默认: 5s)
```

## 💻 编程接口

### 基本使用

```golang
package main

import (
    "context"
    "fmt"
    "time"
    
    "github.com/xiecang/speedtest-clash/speedtest"
    "github.com/xiecang/speedtest-clash/speedtest/models"
)

func main() {
    // 创建测速实例
    t, err := speedtest.NewTest(models.Options{
        ConfigPath:       "config.yaml",
        DownloadSize:     100 * 1024 * 1024, // 100MB
        Timeout:          10 * time.Second,
        Concurrent:       8,
        SortField:        models.SortFieldBandwidth,
        CheckTypes:       []models.CheckType{
            models.CheckTypeGPTWeb,
            models.CheckTypeNetflix,
            models.CheckTypeCountry,
        },
    })
    if err != nil {
        panic(err)
    }
    defer t.Close()

    // 执行测速
    ctx := context.Background()
    results, err := t.TestSpeed(ctx)
    if err != nil {
        panic(err)
    }

    // 获取有效节点
    aliveProxies, err := t.AliveProxiesWithResult()
    if err != nil {
        panic(err)
    }

    fmt.Printf("测速完成，共 %d 个有效节点\n", len(aliveProxies))
}
```

### 高级配置

```golang
// 自定义缓存
cache := speedtest.NewDefaultCache()
defer cache.Close()

// 自定义测速选项
options := models.Options{
    ConfigPath:          "https://example.com/config.yaml",
    DownloadSize:        50 * 1024 * 1024,  // 50MB
    Timeout:             15 * time.Second,
    Concurrent:          16,
    SortField:           models.SortFieldTTFB,
    LivenessAddr:        "https://speed.cloudflare.com/__down?bytes=%d",
    NameRegexContain:    "香港|台湾|新加坡",
    NameRegexNonContain: "测试|临时",
    URLForTest:          []string{"https://www.google.com", "https://www.youtube.com"},
    CheckTypes: []models.CheckType{
        models.CheckTypeGPTWeb,     // ChatGPT Web 端
        models.CheckTypeGPTAndroid, // ChatGPT Android 端
        models.CheckTypeGPTIOS,     // ChatGPT iOS 端
        models.CheckTypeDisney,     // Disney+ 解锁
        models.CheckTypeNetflix,    // Netflix 解锁
        models.CheckTypeGemini,     // Google Gemini 解锁
        models.CheckTypeCountry,    // 地理位置检测
    },
    Cache: cache,
}

t, err := speedtest.NewTest(options)
```

### 结果处理

```golang
// 获取所有测速结果
allResults, err := t.ProxiesWithResult()

// 获取有效节点
aliveResults, err := t.AliveProxiesWithResult()

// 导出为 YAML
err = t.WriteToYaml("result.yaml")

// 导出为 CSV
err = t.WriteToCsv("result.csv")

// 获取 JSON 格式
jsonData, err := t.AliveProxiesToJson()

// 打印统计信息
t.LogNum()  // 显示统计信息
t.LogAlive() // 显示有效节点表格
```


## 📄 许可证

[MIT](LICENSE)
