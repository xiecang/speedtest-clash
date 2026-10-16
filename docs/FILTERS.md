# 节点筛选

统一筛选器支持 cvt 表达式，以及 V1 的 Go 名称正则。二者同时配置时取交集；旧正则保留大小写、锚点、`(?i)` 等 Go 语义，不转换成 JavaScript 正则。

```sh
node-quality convert -source nodes.yaml -format clash-yaml -filter 'name:香港 and not type=ss'
node-quality evaluate -source nodes.yaml -probe-url https://your-probe.example -filter 'name:香港'
```

HTTP：`POST /api/v2/convert?format=clash-yaml&filter=...`，表达式须 URL 编码。兼容查询参数 `name_regex_contain`、`name_regex_not_contain`；CLI 对应 `-name-regex-contain`、`-name-regex-not-contain`。HTTP 认证与严格解析行为保持原有契约，同时只允许一个转换请求，其余返回 429。

Go API：`CompileSelector(ctx, SelectionOptions{Filter: ...})`；`Match` 返回与输入对齐的掩码，`Prune` 删除不匹配节点和依赖它们的全部路径。`ConvertSelected` 在严格解析后调用 `Prune`。`PreparePlan` 为重复测量预编译选择器；修改筛选字段后须重新调用它。

表达式沿用 cvt：`not` > `and` > `or`，支持括号。省略字段时匹配 `name`；`:` 为 Unicode 忽略大小写的部分匹配，`=` 为区分大小写的全匹配；`!:`、`!`、`!=` 为对应否定。支持点/括号路径、数组正负下标、`length` 和 `$` 递归匹配。正则支持前后查找、反向引用与 Unicode 属性。空格用字符类（例如 `Hong[ ]Kong`）；Go 的 `(?i)` 只用于旧正则入口。

测量筛选仅控制根目标，运行所需依赖仍加载；全部排除时不访问探针、不下载。筛选字段不进入 NodeKey、配置修订或测量版本，不延长缓存有效期。转换筛选会删除节点及依赖路径，不能通过补依赖重新引入已排除节点。node-quality 输出节点列表，不生成分组，因此没有 `hide` 参数；完整订阅中的隐藏由 xcproxy 处理。

纯表达式结果按“表达式 + 实际 JSON 内容（含当前展示名称）”的 SHA-256 复用，跨轮次和同进程的重复构建共享，最多保留 100,000 条并按插入顺序淘汰；只存摘要和布尔值，不保存节点凭据。修改表达式、名称或配置会重新计算，不依赖调用方宣称的 NodeKey/修订。地区证据先按原有效期生成名称，再查筛选缓存；此缓存不刷新测量或地区证据。

边界：表达式/旧正则各最多 4096 字节，JS 每批最多 200 个节点、1 MiB JSON；最多两个隔离执行器，每个 WASM 内存 64 MiB、JS 堆 32 MiB。每次执行上限 2 秒，超时或错误会销毁实例并返回失败，不输出部分结果。执行器不开放文件、网络或环境变量。来源解析和调用者保留的节点另占内存，这些数值不是整个进程的 RSS 上限。
