# deepseek-search

[English](README.en.md) | 中文

把 DeepSeek 官方的 **Search / Fetch API** 封装成一个可独立分发的 MCP Server（Go，单二进制）。

- `deepseek_search` — 网页搜索，返回带权威度与相关性评分的结构化结果
- `deepseek_fetch` — 抓取单个网页，返回标题 + Markdown 正文
- 传输：**stdio** 与 **Streamable HTTP**（HTTP 支持 Bearer / `X-API-Key` 鉴权）
- API Key 支持 **环境变量** 与 **CLI 参数** 传入（flag 优先）

接口定义、错误码、计费与限流以 DeepSeek 官方文档为准：<https://api-docs.deepseek.com/guides/search>。

---

## 安装

```bash
make build                 # 产出 ./bin/deepseek-search
# 或
go install github.com/dotSlashLu/deepseek-search-mcp@latest
```

> `go install` 会按模块路径末段把命令命名为 `deepseek-search-mcp`；想要短名字就用 `make build`（或下载 release 里的 `deepseek-search` 二进制，或自行重命名）。注意模块路径大小写敏感：必须写 `dotSlashLu`，全小写会因路径不匹配而失败。

需要 Go >= 1.25（官方 MCP Go SDK 的要求）。交叉编译（`make release`）产出各平台单文件二进制，无运行时依赖。

不想装 Go 的话，直接从 [Releases](https://github.com/dotSlashLu/deepseek-search-mcp/releases/latest) 下载对应平台的压缩包，解压即得 `deepseek-search`。

## 让 Agent 帮你装

把这段丢给 Agent（Claude Code / Cursor / Codex 都行），选包、安装、注册它自己会搞定：

```text
帮我装一下 deepseek-search 这个 MCP server：https://github.com/dotSlashLu/deepseek-search-mcp/releases/latest
装到 ~/.local/bin，用 stdio 注册到我的 MCP 客户端，DEEPSEEK_API_KEY=<DEEPSEEK_KEY>（没填就问我）；下载后用同目录的 checksums.txt 校验 SHA256。
装完确认 tools/list 里有 deepseek_search 和 deepseek_fetch，再搜一次 "hello world" 给我看。
```

## 使用

### stdio

```bash
DEEPSEEK_API_KEY=sk-xxxx ./bin/deepseek-search
```

客户端配置：

```json
{
  "mcpServers": {
    "deepseek-search": {
      "command": "/absolute/path/to/deepseek-search",
      "env": { "DEEPSEEK_API_KEY": "sk-xxxx" }
    }
  }
}
```

Claude Code 也可以用命令行注册：

```bash
claude mcp add deepseek-search -e DEEPSEEK_API_KEY=sk-xxxx -- /absolute/path/to/deepseek-search
```

### Streamable HTTP

```bash
DEEPSEEK_API_KEY=sk-xxxx MCP_HTTP_AUTH_TOKEN=my-token \
  ./bin/deepseek-search --transport http --addr 127.0.0.1:8080
```

- MCP 端点：`http://127.0.0.1:8080/mcp`
- 健康检查：`http://127.0.0.1:8080/healthz`（无需鉴权）
- 鉴权：`Authorization: Bearer my-token` 或 `X-API-Key: my-token`

客户端配置：

```json
{
  "mcpServers": {
    "deepseek-search": {
      "type": "http",
      "url": "http://127.0.0.1:8080/mcp",
      "headers": { "Authorization": "Bearer my-token" }
    }
  }
}
```

未配置 `--http-auth-token` 时服务仍可启动（默认只监听 `127.0.0.1`）并打印 WARN；用 `--http-auth-required` 可以强制要求鉴权，此时没有 token 会拒绝启动。

## 配置项

优先级：**CLI flag > 环境变量 > 默认值**。

| Flag | 环境变量 | 默认值 | 说明 |
|---|---|---|---|
| `--transport` | `MCP_TRANSPORT` | `stdio` | `stdio` 或 `http` |
| `--api-key` | `DEEPSEEK_API_KEY` | — | DeepSeek API Key，必填（`--version` 除外） |
| `--base-url` | `DEEPSEEK_BASE_URL` | `https://api.deepseek.com` | API 基地址，可用于代理或自建网关 |
| `--timeout` | `MCP_TIMEOUT` | `60s` | 单次 API 调用超时（含重试） |
| `--max-retries` | `MCP_MAX_RETRIES` | `2` | 408/429/5xx 的重试次数 |
| `--addr` | `MCP_HTTP_ADDR` | `127.0.0.1:8080` | HTTP 监听地址 |
| `--http-path` | `MCP_HTTP_PATH` | `/mcp` | MCP 端点路径 |
| `--http-auth-token` | `MCP_HTTP_AUTH_TOKEN` | — | HTTP 鉴权 token |
| `--http-auth-required` | `MCP_HTTP_AUTH_REQUIRED` | `false` | 没有 token 时拒绝以 HTTP 启动 |
| `--stateless` | `MCP_HTTP_STATELESS` | `false` | 关闭 MCP 会话（多实例/无状态部署） |
| `--http-json-response` | `MCP_HTTP_JSON_RESPONSE` | `false` | 用 `application/json` 应答而不是 SSE 流 |
| `--http-session-timeout` | `MCP_HTTP_SESSION_TIMEOUT` | `30m` | 空闲会话回收时间，`0` 表示永不回收 |
| `--http-allow-non-local-host` | `MCP_HTTP_ALLOW_NON_LOCAL_HOST` | `false` | 允许「监听回环但 Host 头非回环」的请求，**反向代理转发原始 Host 时必须打开** |
| `--log-level` | `MCP_LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` |
| `--version` | — | — | 打印版本 |

日志一律写到 **stderr**（stdio 模式下 stdout 是唯一的协议通道）。

## MCP 工具

### `deepseek_search`

| 参数 | 类型 | 必填 | 默认 | 说明 |
|---|---|---|---|---|
| `query` | string | ✅ | — | 1–128 字符；支持 `site:` `-site:` `-term` `-"phrase"` `"exact phrase"` `lang:` `after:` `before:` |
| `count` | int | | 10 | 1–50 |
| `ori_question` | string | | — | 触发本次搜索的原始问题，可提升查询理解 |
| `localization` | string | | — | 本地化用 IP，仅当用户给出时才传 |
| `need_content` | bool | | `false` | 为 `true` 时每条结果附带网页正文 |
| `max_content_chars` | int | | 4000 | ≥0，`0` 表示不限制正文长度 |

返回可读 Markdown（标题、URL、站点、发布时间、权威度、相关性、snippet、summary，可选正文）以及结构化元数据。**搜索无结果不是错误**，会返回 `No results found…`。

### `deepseek_fetch`

| 参数 | 类型 | 必填 | 默认 | 说明 |
|---|---|---|---|---|
| `url` | string | ✅ | — | 1–8192 字符，绝对 HTTP/HTTPS URL，不能带账号密码 |
| `max_chars` | int | | 0 | ≥0，`0` 表示不截断 |

返回 `Title:` / `URL:` + Markdown 正文；截断处会显式标注 `[... truncated at N characters ...]`。

两个工具都标记为只读、幂等；调用失败以 tool error 返回，错误信息包含状态码含义，模型可自行纠正。

## 部署

单二进制，不需要运行时依赖。推荐三段式：**二进制 + 密钥文件 + 进程守护**，公网入口交给 nginx。

### 1. supervisor（或 systemd）托管

把二进制放在任意目录（示例用 `/opt/deepseek-search`），密钥写在 0600 的文件里，由启动脚本读入环境，避免出现在进程列表和守护进程配置里：

```ini
[program:deepseek-search]
command=/opt/deepseek-search/run.sh
directory=/opt/deepseek-search
autostart=true
autorestart=true
startsecs=2
stopsignal=TERM
stopwaitsecs=15
redirect_stderr=true
stdout_logfile=/var/log/deepseek-search.log
stdout_logfile_maxbytes=10MB
stdout_logfile_backups=3
```

`run.sh`（755，密钥文件同目录 600）：

```sh
#!/bin/sh
set -eu
set -a
. /opt/deepseek-search/config.env   # DEEPSEEK_API_KEY / MCP_HTTP_AUTH_TOKEN ...
set +a

exec /opt/deepseek-search/deepseek-search \
  --transport http \
  --addr 127.0.0.1:8080 \
  --http-path /mcp \
  --http-auth-token "$MCP_HTTP_AUTH_TOKEN" \
  --http-allow-non-local-host \
  --http-json-response
```

### 2. nginx 反代到公网

```nginx
location /mcp/deepseek-search {
    # 去掉公网前缀，上游端点始终是 /mcp
    rewrite ^/mcp/deepseek-search/?(.*)$ /mcp$1 break;

    proxy_pass http://127.0.0.1:8080;
    proxy_http_version 1.1;
    proxy_set_header Connection "";
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;

    proxy_connect_timeout 10s;
    proxy_send_timeout 300s;
    proxy_read_timeout 300s;   # SSE 长连接
    proxy_buffering off;       # SSE 必须关缓冲
    client_max_body_size 8m;
}
```

健康检查可以单独透出：

```nginx
location = /mcp/deepseek-search/healthz {
    proxy_pass http://127.0.0.1:8080/healthz;
    access_log off;
}
```

### 3. 升级

运行中的二进制不能直接覆盖（`ETXTBSY`），顺序是：`supervisorctl stop <program>` → 覆盖二进制 → `supervisorctl start <program>`。改密钥只动 `config.env`，然后 `supervisorctl restart <program>`。

> 两个反代场景的必读开关：`--http-allow-non-local-host`（否则转发原始 Host 的请求一律 403）和 `--http-session-timeout`（否则长跑进程的会话永不回收）。原因与实现细节见 [docs/engineering.md](docs/engineering.md)。

## 计费与限流

- Search / Fetch 均按**成功请求**计费：**$0.75 / 1000 次**；失败请求、空搜索结果、超时不计费，`count` 不影响单价
- 账号级 QPS：Search 5、Fetch 5，各自独立计数；Fetch 另有按域名的 QPS 限制，超限返回 429
- 只有 HTTP 200 且有结果才算计费成功；状态码含义见官方文档

## 开发

```bash
make test     # 单元测试
make vet      # go vet
make smoke    # 端到端冒烟：stdio + HTTP（自带 mock API，不需要真 key）
make schema   # 打印下发的工具描述与 JSON Schema
make release  # 交叉编译多平台
```

测试覆盖、设计取舍、上游 API 观察记录等工程笔记见 [docs/engineering.md](docs/engineering.md)。

## 许可

MIT
