# 工程笔记

这份文档记录**为什么这么做**：工具面的设计取舍、传输层的坑、上游 API 的实测观察、测试策略。
使用与部署步骤看 [README](../README.md)。

内容随代码演进，标注日期的段落是当时的实测结论。

---

## 1. 工具面设计

工具的描述、参数名与 schema 是模型唯一的「接口说明书」，所以这里的每一处措辞都对应一种失败模式。

| 设计 | 防的失败模式 |
|---|---|
| 只有 `query` / `url` 必填（schema 推断规则：带 `omitempty` 的字段不进 `required`） | 模型只给必填就能跑通，客户端本地校验不拦人 |
| 用 `bool` / `int`，不用字符串枚举 | 防模型传 `"true"` / `"yes"` / `"ten"` |
| 描述里**穷举**支持的运算符 `site: -site: -term -"phrase" lang: after: before:` | 防模型自造 `intitle:`、`filetype:` 这类不存在的运算符，导致静默空结果 |
| 描述里写清「需要全文才 `need_content=true`；读已知 URL 用 fetch」 | 防一次性拉多页正文打爆上下文、也防多花钱 |
| 描述里带计费与 5 QPS | 让模型倾向少而准的查询 |
| `authority` / `rerank_score` 的取值含义写进 schema，正文里再渲染成 `authority: 3/4 \| relevance: 2.40` | 让模型能对来源可信度排序，而不是把结果平等对待 |
| 空结果 = 正常文本 + 「换更宽泛的词」提示，**不是 error** | 防空结果触发重试死循环（官方语义里空结果也不计费） |
| 错误信息翻译成可行动的话（如 `insufficient account balance`） | 模型能一轮自纠 |
| `structuredContent` 只放元数据，正文只出现在 `content` 文本里 | 防同一段正文进两次上下文、token 翻倍 |
| 截断处物理写入 `[... truncated at N characters ...]` | 防模型把半截正文当全文下结论 |
| 工具名带 `deepseek_` 前缀 + `readOnlyHint`/`idempotentHint` | 与其他搜索类 MCP server 共存时不撞名；只读提示让客户端敢免确认调用 |
| 结果被 `count` 限制住时标注 `(count limit N reached — more may exist)` | 给模型一个「可以要更多」的明确信号 |

### 模型可见文案里不写厂商名（2026-10-17 的决定）

**规则**：工具描述、参数描述、annotation 标题、server instructions 里不出现厂商名；厂商、计费归属、限流等运维信息只放在人能看到的地方（README、`--help`、启动日志、错误里的 API 路径）。

**理由**：

- 描述是「任务界面」而非产品目录，厂商名对「用不用、怎么调」没有可执行价值
- 避免品牌偏见影响工具选择（尤其同装多个搜索 server 时）
- `"DeepSeek's in-house search engine"` 这类形容词无法验证，只占 token

**保留的例外**：工具名仍带 `deepseek_` 前缀（名字要唯一、可辨识），错误信息里仍出现上游端点路径（排障价值 > 品牌中立）。`TestModelFacingTextIsVendorNeutral` 把这条约定固定成回归断言。

## 2. Schema 的实现方式

`internal/mcpserver/schema.go` 的做法是「**推断 + 打补丁**」：先用 `jsonschema-go` 从 Go 结构体推断 schema（类型、required、描述），再补上推断表达不了的约束。

- **为什么不用手写 JSON Schema**：手写容易和 Go 类型脱节，改一个字段要改两处、错了会直接拒绝所有调用。
- **为什么要补**：SDK 的类型推断只把 `jsonschema` struct tag 当 description 用，required 只由 `omitempty` 决定（`jsonschema-go` 的 `infer.go`），因此 `count` 的 1–50、`query` 的 128 字符上限这些**边界不会自动出现**。补上后客户端可以本地拒绝（我们这边也会拒），少一次往返。
- **默认值靠 schema 的 `default` 注入**：SDK 在解析入参时会应用 schema 默认值，所以 `count` 省略即 10、`max_content_chars` 省略即 4000，处理器不用再写一遍「没传就取默认」。
- **`MaxContentChars` 为什么是指针**：需要区分「省略」（schema 默认 4000）和「显式传 0」（不限制）。指针类型会被推断成 `["null","integer"]`，所以补丁里归一化成 `integer`。处理器仍保留「nil → 4000」的兜底，万一某个客户端路径没走默认值注入，也不会把整页正文倒进上下文。

**已知取舍**：越界参数会由 SDK 校验层拒绝，错误文案偏机器化（`validating /properties/count: maximum: 99 is greater than 50.000000`）。如果更看重友好文案（`count must be between 1 and 50`），去掉 schema 里的 `minimum`/`maximum` 即可，代价是失去客户端本地校验。

## 3. 传输层的坑

均实测于官方 Go SDK v1.8.0。

| 现象 | 原因 | 处置 |
|---|---|---|
| 反向代理转发原始 Host 时**一律 403**，看着像鉴权失败 | 传输层默认启用 DNS-rebinding 防护：拒绝「监听在回环地址但 Host 头不是回环」的请求 | `--http-allow-non-local-host`（或在代理层不转发 Host） |
| 长跑进程里会话越积越多 | `StreamableHTTPOptions.SessionTimeout` 零值 = 永不回收 | `--http-session-timeout`（默认 30m） |
| POST 只有 `Accept: application/json` 时报 400 | Streamable HTTP 规范要求 POST 的 `Accept` 同时包含 `application/json` 与 `text/event-stream` | 客户端按规范发两个值；`--http-json-response` 只改变**响应**形态，不改这条校验 |
| stdio 模式灌完请求立刻关闭 stdin，响应丢失 | 传输在 EOF 后结束，未刷出的响应随进程退出 | 正常 MCP 客户端都保持 stdin 打开；脚本化调用参考 `scripts/stdio_client.py` |

`--http-json-response` 对我们是纯收益：本服务从不主动推送 server→client 消息，用 `application/json` 应答可以绕开 SSE 在代理层的缓冲/超时问题。

## 4. API 客户端行为

- **重试**：仅 408 / 429 / 5xx，指数退避 + 抖动（50%–100% 区间），尊重 `Retry-After`（秒数或 HTTP 日期两种格式），上限 8s，默认重试 2 次（共 3 次尝试）。其余 4xx 不重试；上下文取消或超时后不再重试。
- **超时**：一次调用（含重试）共享一个 `--timeout`，默认 60s。
- **错误映射**：把官方状态码翻译成一句可行动的话，例如 402 → `insufficient account balance`、429 → `rate limit exceeded (Search and Fetch allow 5 QPS per account)`；错误以 tool error（`isError: true`）返回，而不是协议层异常，模型能看到原因。
- **响应体上限**：32 MiB，防止异常响应打爆内存。
- **计费无关性**：失败请求不计费，重试只在失败时发生，因此重试机制不会产生额外费用。
- **日志不含密钥**：`debug` 只记录路径、耗时、HTTP 状态与重试情况。

## 5. 上游 API 观察记录

| 日期 | 观察 |
|---|---|
| 2026-10-17 | `fetch` 对 DNS 不可达的域名返回 **500 + request_id**（文档写的是 404）。映射会显示为 internal error，并保留 request_id 便于向官方报障 |
| 2026-10-17 | `fetch` 返回的 Markdown 带反斜杠转义（`\/`、`\:`、`\#`），是上游转换器的输出风格，本服务原样透传不做清洗 |
| 2026-10-17 | `search` 的 `rerank_score` 是完整浮点（如 `2.7447335294150137`）；文本渲染取两位小数，结构化输出保留原值 |
| 2026-10-17 | 空 `results` 数组返回 200，符合「空结果不计费」的语义 |

## 6. 测试策略

```
internal/deepseek  客户端：请求构造与请求头、need_content 缺省、空结果、
                   10 种错误码映射、重试次数与退避、超时/取消、URL 与参数校验
internal/mcpserver 工具面：tools/list、schema 边界与默认值、厂商中立回归、
                   max_content_chars=0 的不限语义、内容截断、tool error 路径、
                   鉴权中间件（Bearer / X-API-Key / 401 挑战）、
                   反代 Host 403 与放行、会话空闲回收、JSON 响应模式、
                   HTTP 端到端（有状态 + 无状态）
internal/config    flag/env/默认值优先级、各类校验错误
```

- **`scripts/smoke.sh`**：拉起 `scripts/mockapi.py`（Search/Fetch 的桩服务）跑真实 stdio 与 HTTP 会话，不需要真 key，也不花钱。stdio 部分由 `scripts/stdio_client.py` 驱动（保持 stdin 打开，见 §3）。
- **`make schema`**（`scripts/dump_schema.py`）：打印当前下发的描述与 schema，改文案后用来做回归对照。

## 7. 目录结构

```
main.go                  CLI 解析、传输选择、优雅退出
internal/config          flag + env 合并与校验
internal/deepseek        Search / Fetch 客户端：重试、错误映射
internal/mcpserver       工具注册、schema、Markdown 渲染、HTTP 鉴权
scripts/                 mock API、stdio 驱动器、冒烟脚本、schema dump
docs/engineering.md      本文档
```

## 8. 分发

`make release` 按 `darwin|linux|windows × amd64|arm64` 交叉编译（`CGO_ENABLED=0`，产物约 12 MB）；打 tag 后 `.github/workflows/release.yml` + `.goreleaser.yaml` 自动出多平台压缩包与 `checksums.txt`。

## 9. 未做 / 可选的后续

- **按请求透传调用方的 DeepSeek Key**（如 `X-DeepSeek-Api-Key`）：当前 Key 只来自服务端配置。要做的话需要在 HTTP 层按请求构造客户端，并把 Key 从日志里彻底排除。
- **客户端限流器**：官方 QPS 只有 5、调用方天然低频，暂时不做；多实例共享同一个账号时才需要。
- **`localization` 的取值校验**：只写了「不要猜」的文案，没有校验是不是合法 IP。
- **`fetch` 的并发/去重**：同一 URL 的重复抓取仍然会计费，可考虑短期缓存（但要注意内容时效性）。
