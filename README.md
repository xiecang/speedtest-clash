# node-quality V2

Go 节点转换、质量检测及受控探针库。Module `github.com/xiecang/node-quality/v2`，包名 `nodequality`，CLI `node-quality`。GPL-3.0-only，第三方原始版权及许可见 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)。V1 源码可通过 `v1-before-v2` 标签查阅，不提供 V1 API 兼容层。

## 职责边界

本库仅负责导入/转换、内存中的检测与报告、受控探针。它不维护业务数据库，不保存检测历史或节点准入状态，不负责 worker 调度、outbox/ACK、排名、分组和订阅发布；这些由 xcproxy 实现。调用者可注入 IP 缓存回调，默认不持久化。CLI 输出到标准输出或显式指定的文件，不会创建质量数据库。

嵌入内核的间接依赖可能包含其缓存存储组件，不等于本库拥有质量数据库。仓库内 `.codegraph/` 是开发工具索引，不属于运行时或发布产物。

## 代码入口

根包只保留公开类型别名和函数转发，调用者继续使用 `nodequality`，节点和报告不经过中间对象复制。

| 目录 | 负责内容 |
|---|---|
| `internal/model` | 节点身份、依赖图、计划和报告数据模型 |
| `internal/codec` | URI / Mihomo / sing-box 编解码、名称引用和规则转换 |
| `internal/source` | 来源获取、暂存、完整校验和分批输出 |
| `internal/engine` | 两个原生内核的能力、批次、拨号及生命周期 |
| `internal/measure` | 轻检、通用/AI/可选服务测量、IP 缓存回调和带宽预算 |
| `internal/probe`、`internal/httpapi` | 受控探针协议和 HTTP 接口 |
| `tests/integration` | 从公开 API 验证兼容性、规模和真实内核行为 |

私有测量断言与实现放在同包；跨组件测试使用公开 API。`internal/clash2singbox` 保留第三方转换代码和版权边界。

## 本地构建

最低 Go **1.25.5**；固定 Mihomo **v1.19.32**、sing-box **v1.14.2**。二者直接嵌入 Go 运行时，不创建 TUN、不改系统路由、不启动每节点子进程。生产不需要 cvt、Deno 或 sing-box 可执行文件。

```sh
make build
make test
make vet
make race
./dist/node-quality capabilities
```

构建必须带 `with_quic,with_utls,with_wireguard,with_gvisor,with_clash_api`（Makefile 已配置）。`capabilities` 展示实际构建标签。缺失内核协议支持或配置不可表达记为 `unknown`，不能当作劣质节点。

```sh
./dist/node-quality convert --source nodes.yaml --format sing-box-json
./dist/node-quality evaluate --source nodes.yaml --core sing-box --probe-url https://probe.example --profile ai --ai-concurrency 10
NODE_QUALITY_TOKEN=your-secret ./dist/node-quality probe-server --listen 127.0.0.1:8070
```

跨机器探针使用 HTTPS；原生 TLS 加 `--tls-cert` / `--tls-key`，或在可信反向代理后部署。H2 测试验证真实协议协商，不接受 H1 回退。

## Go API

```go
import nq "github.com/xiecang/node-quality/v2"

decoded, err := nq.Decode(content)
output, err := nq.Encode(decoded.Nodes, nq.SingBoxJSON)
output, diagnostics, err := nq.Convert(content, nq.ClashYAML)
plan := nq.DefaultPlan()
plan.ProbeURL = "https://probe.example"
reports, err := nq.Evaluate(ctx, decoded.Nodes, plan, nq.NewBudget(10<<20))
handler := nq.ProbeHandler(nq.ProbeOptions{Token: token})
```

共享同一个 `Budget` 才能保证整个 worker 的额度。它在读取前等待额度，按实读字节计费，短读退回未用额度，取消不会退回已读字节。HTTP/SSE/WS transport 按节点、内核和协议独立；取消/批次完成关闭空闲连接和内核实例。代理链整体检测；`TargetKeys` 指定待测链路根，依赖只装载。单批根及依赖最多 200。

指标定义与缺失语义见 [docs/METRICS.md](docs/METRICS.md)。格式、报告、探针及接口详见 [docs/V2.md](docs/V2.md)。与本地 cvt 的行为参考位于 `testdata/cvt-reference.json`；Deno 仅用于开发时重新生成参照。

## 来源加载与轻检

`LoadSourceDetailed(ctx, source, client, emit)` 返回 `SourceSummary`，先完整校验代次/依赖，再分批输出可用节点；独立不支持条目明确诊断。严格Decode/Convert接口不变。支持Data URL、旧Proxy字段与SSR/Mieru/SS插件URI；格式或固定内核不能表达的设置不静默丢弃。地区、提示节点过滤及展示名称由xcproxy负责。

`Plan.Profile` 支持 general、ai、availability、services。availability仅连通性，不能提供general通过；services最多3个可选检查，CheckRevision独立于核心计划，匹配旧结果保留原测量时间。AI仅复用同节点修订/内核版本/worker/environment及当前定义的新鲜下载，仍重做健康与三协议长流。详情见 [docs/METRICS.md](docs/METRICS.md)。

节点筛选、V1 正则兼容与使用边界：参见 [docs/FILTERS.md](docs/FILTERS.md)。

### Worker-wide parallel budget

`NewBudget(bytesPerSecond)` retains its single-node download slot. Use `NewBudgetWithConcurrency(100<<20, 8)` for eight concurrent download nodes sharing one 100 MiB/s budget; handle its validation error and share the returned budget across every job and core in the worker. `Budget.Stats()` reports actual download reads, aggregate slot waiting time and active download nodes. Report metrics include the configured node concurrency separately from streams per node. Queue waiting remains excluded from goodput.

### Complete empty source results

`LoadSourceRequestDetailed` strictly loads one `SourceRequest`, including all recursive providers, while honoring `AllowLocalFiles` (false by default). `ErrNoNodes` identifies a completely parsed generation with no supported nodes; fetch, malformed document, dependency and provider failures remain separate errors. Existing `LoadSourceDetailed` behavior is preserved, including returning an error for an empty generation. Business decisions such as empty-subscription archival belong to xcproxy.
