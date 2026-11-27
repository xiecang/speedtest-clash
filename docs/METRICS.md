# 评估指标与故障归因参考

本文档定义 `Report`（`schema_version = 2`, `metric_version = 2`）的指标口径、长连接验证模型与故障归因规则。

所有耗时 JSON 字段均以 **纳秒（ns）** 表示；所有速率与限额字段均以 **字节/秒（bytes/s）** 表示（`1 MiB/s = 1,048,576 bytes/s`）。当样本数为 `0` 时，对应中位数为 `0`，表示“未测得”而非零延迟。

---

## 1. 下载与连通性指标（`Metrics`）

| JSON 字段 | 说明 |
|---|---|
| `health_attempts` | 探针 `/api/v2/probe/health` 请求总次数（`availability` 模式为 `1`，`general`/`ai` 模式最多 `5`） |
| `health_failures` | 健康请求失败次数（`general`/`ai` 允许最多 1 次偶发非致命抖动） |
| `cold_health_samples` | 新建连接（`GotConn.Reused == false`）成功的健康样本数 |
| `reused_health_samples` | 复用已有连接成功的健康样本数 |
| `cold_health_median_ns` | 冷连接从发起请求到收到完整响应头的耗心中位数（含代理握手与 TLS 成本） |
| `reused_health_median_ns` | 复用连接同口径耗心中位数 |
| `download_headers_median_ns` | 并发下载流从发起请求到收到 HTTP 200 响应头的中位数耗时 |
| `download_header_samples` | 成功收到响应头的下载请求数 |
| `download_reused_header_samples` | 下载请求中复用已有连接的数量 |
| `download_node_concurrency` | 当前共享 `Budget` 配置的并发下载节点槽位数 |
| `bandwidth_queue_wait_ns` | 等待获取 `Budget` 下载节点槽位的排队墙钟时间（不计入 `download_duration_ns`） |
| `download_bytes` | 应用层 `Read` 实际读取的总字节数 |
| `download_duration_ns` | 获得下载槽位后至全部下载流结束的墙钟耗时 |
| `download_end_reason` | 结束原因：`budget`（读满目标字节）、`window`（到达采样时间窗上限）、`partial_budget`（多流并发下多数流正常完成且达到半数预算）、`error`（异常中断） |
| `goodput_bytes_per_second` | 有效应用层吞吐：`download_bytes / download_duration_seconds` |
| `rate_limit_bytes_per_second` | 共享 `Budget` 配置的限速上限（`0` 表示不限速） |
| `aggregate_limiter_wait_ns` | 所有并发下载流等待令牌桶额度的累计耗时之和 |
| `rate_limited` | 本次采样期间是否实际发生过本地限速等待 |

---

## 2. AI 长连接流指标（`StreamResult`）

在 `profile = "ai"` 下，评估器同时发起 `h1`（HTTP/1.1 SSE）、`h2`（强制验证 `ProtoMajor == 2` 的 HTTP/2 SSE）与 `websocket` 三条受控流：

- **主动静默恢复测试**：探针服务端在流周期中段插入一段受控静默期（上限 25 秒，避免触发常规中间网关默认空闲超时），发送 `idle` 标记后暂停推流，静默期结束后继续推送连续序号 `data` 帧并以 `done` 帧收尾。
- **通过条件（`status = "pass"`）**：三条协议流均须满足序号连续无跳帧、观测到合法 `idle` 标记（`idle_marker_seen`）、静默后成功恢复接收数据帧（`resume_observed`）、主动阶段静默不超过 10 秒（`max_active_silence_ns`），且收到 `done` 标记时总时长达到目标时长阈值。
- **阶段诊断**：若流失败，`failure_stage` 记录中断所处阶段（`connect`、`active`、`idle`、`resume`、`resumed`、`completion`），`failure_reason` 记录脱敏后的固定错误类别（`timeout`、`eof`、`connection_reset`、`websocket_close`、`invalid_frame`、`cancelled`、`read_error`），绝不导出可能含节点凭据的底层原始错误文本。

---

## 3. 故障归因（`FailureOrigin`）与状态语义

| `failure_origin` | `status` | 含义 |
|---|---|---|
| `node` | `fail` | 已证实的节点自身协议或凭据错误（如对端返回代理认证要求 `node_auth_required`） |
| `environment` | `unknown` | 评估端本机资源耗尽（如 `EMFILE`/`ENOBUFS`）、本地路由不可达（`ENETUNREACH`）或上下文主动取消，标记 `infrastructure_failure = true` |
| `probe` | `unknown` | 直连探针对照检查失败、探针返回非预期 HTTP 状态码（`probe_http_<code>`）或探针会话满载，标记 `infrastructure_failure = true` |
| `unknown` | `unknown` | 链路超时、连接重置或未注册内核能力（`unsupported_core` / `unsupported_configuration`），无法单独断定为节点配置损坏 |

---

## 4. 证据复用与独立服务检查

- **AI 阶段下载证据复用**：当 `Plan.GeneralEvidence` 提供同一节点、同一内核版本、同一 `WorkerID`/`Environment` 且 `EvaluationRevision` 匹配的新鲜 `general` 通过报告时，`ai` 评估会直接复用其下载指标与出口 IP 结果（标记 `general_reused = true`，保留原始 `general_measured_at` 时钟），仅重新执行健康检查与三路长连接压测。
- **独立服务可达性（`services`）**：每个 `ServiceCheck`（支持 `http`、`openai-mobile`、`anthropic`、`imgur`）拥有独立的 `CheckRevision`，增删服务检查不会使已有的 `general` 下载或 `ai` 长连接证据失效。
