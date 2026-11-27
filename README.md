# node-quality

`node-quality` 是一个纯 Go 实现的代理节点多格式无损转换、双内核真实流量质量评估与受控探针引擎，可作为独立 CLI 工具运行，也可作为无状态的 Go SDK 嵌入上层系统。

## 核心优势

- **原生嵌入双内核，零外部进程依赖**：内置 **Mihomo `v1.19.32`** 与 **sing-box `v1.14.2`** 出站运行时，进程内直接拨号，无需外部可执行文件、不接管系统路由、不创建全局 TUN 网卡。
- **单趟无损双向转换**：原生实现 Clash YAML ↔ sing-box 1.14 JSON ↔ 分享链接（URI）互转，完整支持 TLS ECH、Reality、uTLS、TCP Brutal、ShadowTLS 链、WireGuard Endpoints、Snell v4、SSH、Hysteria2 端口跳跃与混淆等现代特性；遇到目标格式无法无损表达的字段直接报错诊断，绝不静默丢弃安全配置。
- **面向真实长连接与 AI 负载的质量评估**：除冷/热连接延迟与并发下载有效吞吐（Goodput）外，内置受控探针协议，同时验证 **HTTP/1.1 SSE、HTTP/2 真协商 SSE、WebSocket** 三路长连接在主动静默窗口（Idle Gap）后的真实存活与恢复能力。
- **严谨的故障归因与带宽预算隔离**：区分节点自身故障（`node`）、本机网络/句柄耗尽（`environment`）、探针设施异常（`probe`）与路径未知故障（`unknown`），避免因本地抖动或探针限流误杀健康节点；内置多任务共享令牌桶 `Budget`，按应用层实读字节精确控速。
- **高性能强类型节点筛选**：基于 [`expr-lang/expr`](https://github.com/expr-lang/expr) 字节码虚拟机与归一化节点视图（`filter.Env`），屏蔽 Clash 与 sing-box 字段命名差异，微秒级完成数万节点筛选与依赖链拓扑剪枝。

---

## 模块结构

| 包路径 | 职责说明 |
|---|---|
| `github.com/xiecang/node-quality/v2` | 质量评估核心入口（`Evaluate`、`Plan`、`Report`、`Budget`、`ServerHandler`） |
| `github.com/xiecang/node-quality/v2/codec` | Clash / sing-box / URI 编解码、订阅源流式安全加载（含 `\|` 多源合并与递归 `proxy-providers`）、路由规则转换 |
| `github.com/xiecang/node-quality/v2/filter` | 基于 `expr` 的强类型表达式筛选与代理链依赖图剪枝（`Prune`） |
| `github.com/xiecang/node-quality/v2/engine` | 嵌入式 Mihomo 与 sing-box 批量出站生命周期管理、二分故障节点隔离与配置校验 |
| `github.com/xiecang/node-quality/v2/probe` | 零缓冲受控探针服务端（`/api/v2/probe/{health,download,sse,ws}`） |

---

## 安装与构建

环境要求：**Go 1.25.5+**。编译时需启用双内核特性构建标签（`Makefile` 已默认配置）：

```sh
TAGS="with_quic,with_utls,with_wireguard,with_gvisor,with_clash_api"

# 构建二进制至 ./dist/node-quality
make build

# 运行测试与竞态检查
make test
make race
```

---

## CLI 快速上手

### 1. 启动受控探针服务

在公网测试目标机部署探针服务（非回环地址监听强制要求设置 `NODE_QUALITY_TOKEN`；支持直接配置 TLS 证书或置于支持 HTTP/2 与 WebSocket 的反向代理之后）：

```sh
NODE_QUALITY_TOKEN="your-secret-token" \
  ./dist/node-quality probe-server \
  -listen 0.0.0.0:8443 \
  -tls-cert /path/to/fullchain.pem \
  -tls-key /path/to/privkey.pem
```

### 2. 订阅转换与筛选

支持本地文件、`http(s)://` 订阅链接（支持 `|` 拼接合并多个订阅）、Base64 订阅及 `data:` URI：

```sh
# 将 Clash 订阅筛选后转换为 sing-box 1.14 JSON
./dist/node-quality convert \
  -source "https://sub1.example.com/clash|https://sub2.example.com/uri" \
  -filter 'name matches "(?i)香港|hk|日本|jp" and tls and udp' \
  -format sing-box-json > singbox-outbounds.json
```

### 3. 节点质量评估

支持四种评估模式（`-profile`）：
- `availability`：单次快速连通性轻检；
- `general`：5 次冷/热连接探针健康检查 + 并发下载吞吐采样 + 出口 IP 画像查询；
- `ai`：在 `general` 合格候选基础上，并发执行 HTTP/1.1 SSE、HTTP/2 SSE 与 WebSocket 长连接静默恢复压测；
- `services`：独立校验 OpenAI、Anthropic、Imgur 或自定义 HTTP 端点的地区解锁与可达性。

```sh
NODE_QUALITY_TOKEN="your-secret-token" \
  ./dist/node-quality evaluate \
  -source ./nodes.yaml \
  -core sing-box \
  -probe-url https://probe.example.com:8443 \
  -profile ai \
  -ai-concurrency 5 \
  -max-bytes-per-second 20971520 > reports.json
```

---

## Go SDK 示例

```go
package main

import (
	"context"
	"fmt"

	nq "github.com/xiecang/node-quality/v2"
	"github.com/xiecang/node-quality/v2/codec"
	"github.com/xiecang/node-quality/v2/engine"
)

func main() {
	ctx := context.Background()

	// 1. 解析任意支持格式（Clash YAML / sing-box JSON / Base64 URI）并自动解析前置代理依赖链
	decoded, err := codec.Decode([]byte(`proxies:
  - {name: hk-01, type: vless, server: hk.example.com, port: 443, uuid: 00000000-0000-4000-8000-000000000001, tls: true}
`))
	if err != nil {
		panic(err)
	}

	// 2. 无损导出为结构化 sing-box 1.14 出站对象或字节流
	sbBytes, err := codec.Encode(decoded.Nodes, codec.SingBoxJSON)
	if err != nil {
		panic(err)
	}
	fmt.Printf("sing-box bytes: %d\n", len(sbBytes))

	// 3. 执行受控探针评估（多并发任务共享同一带宽预算）
	plan := nq.DefaultPlan()
	plan.Core = engine.SingBox
	plan.Profile = "general"
	plan.ProbeURL = "https://probe.example.com:8443"
	plan.ProbeToken = "your-secret-token"
	plan.Filter = `tls and port == 443`

	budget := nq.NewBudget(50<<20, 4) // 共享 50 MiB/s 上限，最多 4 节点并发下载
	reports, err := nq.Evaluate(ctx, decoded.Nodes, plan, budget)
	if err != nil {
		panic(err)
	}
	for _, r := range reports {
		fmt.Printf("node=%s status=%s goodput=%.2f MiB/s\n",
			r.NodeKey[:12], r.Status, r.Metrics.GoodputBytesPerSecond/(1<<20))
	}
}
```

---

## 进阶文档

- [节点筛选表达式语法参考 (`docs/FILTER.md`)](docs/FILTER.md)
- [评估指标与故障归因定义 (`docs/METRICS.md`)](docs/METRICS.md)
