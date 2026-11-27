# 节点筛选语法参考

`filter` 包采用两层筛选机制：
1. **RE2 名称正则（`NameRegexContain` / `NameRegexNotContain`）**：使用 Go 标准库线性时间 `regexp`，始终匹配节点原始名称（`Node.Name`）。
2. **结构化布尔表达式（`Filter`）**：基于 [`github.com/expr-lang/expr`](https://github.com/expr-lang/expr) 编译期强类型检查与字节码执行，单条表达式最大长度 4096 字节、AST 节点上限 256。

---

## 归一化上下文变量（`filter.Env`）

表达式针对归一化的 `filter.Env` 求值，调用方无需区分 Clash YAML（如 `type: ss`, `port: 443`）与 sing-box JSON（如 `type: shadowsocks`, `server_port: 443`）的底层字段差异：

| 变量名 | 类型 | 说明 |
|---|---|---|
| `name` | `string` | 节点名称（调用 `MatchDisplay` 传入展示名映射时为展示名，否则同 `raw_name`） |
| `raw_name` | `string` | 始终为节点原始名称 |
| `type` | `string` | 归一化小写协议名：`ss`, `ssr`, `vmess`, `vless`, `trojan`, `hysteria`, `hysteria2`, `tuic`, `anytls`, `wireguard`, `socks5`, `http`, `mieru`, `snell`, `ssh`, `masque`, `sudoku`, `trusttunnel`, `direct` |
| `server` | `string` | 服务器域名或 IP（WireGuard 自动提取首个 Peer 地址） |
| `port` | `int` | 归一化固定端口号（仅配置端口跳跃范围而无单端口时为 `0`） |
| `network` | `string` | 归一化小写传输层：`tcp`, `ws`, `grpc`, `h2`, `http`, `httpupgrade`, `quic`, `xhttp`, `udp` |
| `tls` | `bool` | 是否启用传输加密（TLS / Reality / ECH / QUIC 内置 TLS / ShadowTLS） |
| `udp` | `bool` | 节点配置是否支持并开启 UDP 转发 |
| `relay` | `bool` | 是否包含前置链式代理依赖（`len(Dependencies) > 0`） |
| `hidden` | `bool` | 节点是否标记为隐藏 |
| `config` | `map[string]any` | 原始节点配置字典（用于访问协议专有字段） |

---

## 常用语法示例

### 1. 逻辑组合
支持 `and`（或 `&&`）、`or`（或 `||`）、`not`（或 `!`）以及圆括号分组：

```text
tls and udp and not relay
(type == "vless" or type == "hysteria2") && !hidden
```

### 2. 名称与域名匹配
支持 `contains`、`startsWith`、`endsWith`、`matches`（Go RE2 正则）以及内置字符串函数 `lower()`、`upper()`、`trim()`：

```text
# 子串包含与排除
name contains "香港" and not (name contains "倍率")

# 忽略大小写正则匹配（字符串内可直接包含空格）
name matches "(?i)hong kong|hk|香港"

# 域名后缀过滤
server endsWith ".example.com"
```

### 3. 协议集合与端口区间
支持 `in`、`not in` 与整数区间 `..`：

```text
type in ["vless", "trojan", "hysteria2", "tuic", "anytls"]
network not in ["ws", "http"]
port in 10000..30000
```

### 4. 访问原始协议字段
低频协议特有属性可通过 `config` 字典读取：

```text
type == "vless" and config["flow"] == "xtls-rprx-vision"
"ech-opts" in config
```

---

## 独立匹配与依赖链剪枝

- **`Selector.Match` / `Selector.MatchDisplay`**：逐节点独立返回 `[]bool`，由上层决定是否通过 `codec.DependencyClosure` 自动拉入未显式选中的前置依赖节点（例如评估链路根时仍需装载其前置跳板）。
- **`Selector.Prune`**：对完整代理图执行 `O(N+E)` 拓扑剪枝——若某个前置跳板节点被筛选器拒绝，所有依赖该跳板的下游节点一并移除，绝不在导出产物中暗中恢复已被排除的前置节点。
