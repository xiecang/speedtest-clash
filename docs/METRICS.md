# Metric 定义版本 2

HTTP/worker schema 仍为 2；`metric_version=2` 描述本页口径，测量计划的 `evaluation_revision` 包含指标版本。xcproxy 的 `ranking_policy_version=4` 独立维护，调排序不使测量过期。本次删除 P50/P90/P95、Jitter、TTFB、RequestFailureRatio、GoodputBPS、RateWait 和 RateCensored 字段，不提供旧字段别名。

所有 duration JSON 字段均为纳秒；带宽均为 **字节/秒**。MiB/s = bytes/s ÷ 1048576；Mbps = bytes/s × 8 ÷ 1000000。零样本的中位数为 0，含义是未测得，不能参与“最快”比较。

| Metrics JSON 字段 | 定义及边界 |
|---|---|
| health_attempts | 实际健康请求数，最多 5；第一请求失败会提前结束健康阶段 |
| health_failures | 这些请求的失败数，含超时/HTTP 非 204；成功数由 attempts−failures 得到 |
| cold_health_samples | 成功且 `httptrace.GotConn.Reused=false` 的健康请求数 |
| reused_health_samples | 成功且复用连接的健康请求数；两类 samples 加 failures 等于 attempts |
| cold_health_median_ns | 冷连接成功健康请求的请求开始至响应头完成中位数，含连接/TLS/代理建立成本；通常仅 1 个样本 |
| reused_health_median_ns | 复用连接成功健康请求的同口径中位数；不与冷连接混算 |
| download_headers_median_ns | 所有 HTTP 200 下载响应的请求开始至完整响应头完成中位数；不是首正文、不是首响应字节，也不是最快值 |
| download_header_samples | 上述成功响应头样本数；正文失败仍有诊断价值，不能因此判下载通过 |
| download_reused_header_samples | 下载头样本中实际复用连接的数量；其余为冷连接；不同复用比例需谨慎比较 |
| bandwidth_queue_wait_ns | 等同一 worker 的唯一带宽节点槽的墙钟时间，不是节点网络延迟 |
| download_bytes | 正文 Read 实际返回字节数，失败前读到的字节也记录；不含 TLS/代理头、重传和内核预读缓存 |
| download_duration_ns | 取得带宽槽后至所有下载流结束的墙钟耗时，含请求/本地限速等待，不含槽排队 |
| download_end_reason | `budget` 达总字节预算；`window` 正常达到时间窗；`error` 提前短读/重置/异常取消等；自身采样取消后的 EOF 归属窗口结束，不把取消竞态认作提前短读；不验证正文内容哈希 |
| goodput_bytes_per_second | 实读字节 ÷ download_duration；是指定探针、并发及窗口条件下有效吞吐，不是链路容量估计 |
| rate_limit_bytes_per_second | 本 worker 配置的应用读取速率；0 表示不限；令牌桶 burst 为 32KiB（不是网卡瞬时上限） |
| aggregate_limiter_wait_ns | 所有下载流等待读取额度的耗时**之和**，含取消前等待，可大于下载墙钟耗时 |
| rate_limited | 至少一条下载流实际等待过额度；配置上限但无等待时为 false。true 时吞吐是受本地限速影响的能力下界 |

读取前取得额度，短读未使用的额度可复用，已读字节不会因取消退回。空闲时未用额度随令牌桶补充失效，不能叠加额外 burst。并行在途读取的完成可能集中发生。限制针对应用 Read；HTTP/TLS/socket 缓存可能已接收更多线路流量，不声称对网卡流量精确限速。

失败健康请求不进入成功耗时中位数，因此必须一起查看分母和失败数；通过准入要求五次健康成功，所以不再把合格节点恒为 0 的健康失败率作为排序项。偶数样本中位数取中间两值平均，不再使用尾分位数。

## 流与阶段时间

`StreamResult` 的 protocol 是 h1/h2/websocket；status 为 pass/fail/unknown。measured_at 为该流开始，duration_ns 至流结束，frames 是连续 data 帧数而非独立试验数。

- `max_active_silence_ns`：读取侧观察到的最大主动阶段静默，包含终止错误/超时前静默，扣除服务端声明的计划 idle 时长；不度量服务端发送抖动，也不能单独发现所有缓冲/突发交付。
- `idle_marker_seen`：收到并校验 idle 标记，**不宣称观测到了完整真实空闲时段**。
- `resume_observed`：idle 标记后收到连续序号数据；不等于验证服务端实际空闲了指定时长。
- 完整通过要求三种协议各自连续序号、idle 标记、恢复数据、结束标记和最短时长。任一可信 fail 优先于其它协议的 unknown；三条协议和帧数不扩充成独立成功次数。
- H2 未协商、探针 HTTP 拒绝/满载、控制失败为 unknown。探针 HTTP 错误保留 `probe_http_<status>`；不能因此隔离节点。父任务取消也不是节点故障。

Report.measured_at 保留整体任务开始；general_measured_at 是健康/下载阶段实际结束，completed_at 是整体结束。`EvidenceAt()` 对 general 使用 general_measured_at，对 AI 使用 completed_at，用于 current 替换、故障否决、隔离恢复、日归属及调度；早期健康失败也记录阶段结束。缓存不刷新任何阶段时钟。

Services 表示指定 URL/正文规则的可访问性，http_status/failure_code 保留诊断，不能证明登录后的产品可用。IP 属性为指定提供方对实际出口的观测，按 IP/地址族/提供方名称、URL和检查版本缓存，unknown 不当失败；信誉和置信度只参与末位软偏好。worker、环境、内核、探针和测量计划不同的吞吐/延迟不能直接当同条件样本。

`failure_origin` 与 `general_failure_origin` 标记失败来源：`node` 仅用于已验证的节点协议/配置错误，当前包括 Mihomo SOCKS5 对端要求未配置的认证并返回 `node_auth_required`；`environment` 表示 worker 本机资源、路由或取消；`probe` 表示探针控制或响应问题；`unknown` 表示传输故障无法归因。只有 `node` 来源报告为 `fail`，其他来源使用 `unknown`。缺少来源字段的旧报告仍可读取，但不能作为已确认的节点故障。


## 轻检及可选服务

availability只请求一次节点health；同批另做探针control。不能把轻检通过当general通过。任一健康请求失败后终止后续下载/缓存复用，保留新故障时钟。services只执行可选服务，不下载/查IP/跑流；结果各有check_revision与2h有效期，匹配版本可复用，不能借复用刷新时钟。

general_reused只表示复用旧下载；general_measured_at及下载指标保持不变，health与AI长流重新执行。复用证据需有足够剩余有效期覆盖后续串行服务/IP及长流，服务检查增减不会改变核心general/AI定义。真实AI故障不会因复用证据随后撤销而被丢弃。Android/iOS特殊响应不能证明网页或登录服务可用；Imgur仅验证图片端点响应头，避免下载整张图片。
