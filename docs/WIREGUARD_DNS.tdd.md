# WireGuard DNS 构造修复：TDD 证据（2026-11-12）

来源：长期运行节点质量排查及用户 `/tdd-workflow`。ali 历史日志有42次相同 `Mihomo NewWireGuard → OpenBatch` nil-pointer 栈；本地合成节点复现 `remote-dns-resolve + dns` 路径。未连接真实节点，也未改动生产。

`ClashConfig` 的共享校验路径加载既有 Mihomo config 初始化，确保 DNS parser 已注册，并在 native device 分配前校验 DNS。单独补初始化仍会在无效 DNS 后泄漏 goroutine，因此同时提前校验。Mihomo 版本仍为 v1.19.32；导入 config 补齐其13项传递模块（含Linux平台 nftables）及 checksum，没有版本升级。其他协议和原配置克隆语义不变。

```sh
go test -mod=readonly -tags with_quic,with_utls,with_wireguard,with_gvisor,with_clash_api ./internal/engine ./internal/codec -run '^TestMihomoWireGuardRemoteDNS$|Wire[Gg]uard' -count=1 -v
```

| 阶段 | 事实及保证 | checkpoint / 日志 |
|---|---|---|
| RED | remote DNS 构造触发 nil pointer，测试实际执行并失败 | ef9256d；wireguard-red.log exit1 |
| 中间修复 | 仅导入 config 后，无效 DNS 3次构造仍遗留78个 WireGuard goroutine | wireguard-initial-fix.log exit1 |
| GREEN | enabled、disabled、invalid_dns 各3次 OpenBatch/Dialer/Close，不 panic，WireGuard goroutine 回到基线；无效输入返回错误 | 2144099；wireguard-green.log exit0 |
| 补充边界 | 非 WireGuard、缺DNS、关闭远程解析、有效DNS、无效DNS、字段类型错误；不改写调用方配置 | 5aee099；codec-coverage.log exit0 |

以上日志在 `/tmp/xcproxy-quality-fix.QD3JOl`。测试使用合成密钥与文档地址。旧源码、最终源码均为真实执行，没有用纯文本模拟冒充构造测试。

实际检查（同一组 tags、`-mod=readonly`）：`go test ./...`、`go vet ./...`、`go test -race ./internal/engine ./internal/codec` 全部 exit0。`go test ./internal/codec -coverprofile=...` 与 `go tool cover -func=...` 显示新校验函数100%，codec整个包13.1%；不宣称达到全库80%。完整模块测试在补充边界测试之前通过，此后 codec 全包与 engine/codec race 再次通过。

Alibaba OCR delegate 选中2个生产文件，当前 Agent 完成审阅，另人工检查2个测试与go.mod/go.sum（代码/依赖共6文件，连本文档共7文件）。无未处理阻塞发现；规则/范围JSON在日志目录。未 push、未部署，未做真实隧道通流或72小时现场验证；本地资源测试只能证明已覆盖构造路径。

Linux amd64/arm64 主程序只读构建初次发现缺 nftables checksum；两模块通过 `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go list -mod=mod -deps ./...` 补齐上游已固定依赖。它属于平台依赖检查失败，不算业务测试 RED。构建命令与最终结果见相邻 xcproxy 的 `docs/LONG_RUNNING_QUALITY.tdd.md`；未在 Linux 机器运行这些二进制。
