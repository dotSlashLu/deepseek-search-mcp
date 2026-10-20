# deepseek-search

[中文](README.md) | English

An MCP server that wraps DeepSeek's **Search / Fetch APIs**, distributed as a single Go binary.

- `deepseek_search` — web search, returning structured results with authority tier and relevance score
- `deepseek_fetch` — fetch one page and return its title plus the content as Markdown
- Transports: **stdio** and **Streamable HTTP** (HTTP supports Bearer / `X-API-Key` auth)
- API key via **environment variable** or **CLI flag** (the flag wins)

Endpoints, error codes, billing and rate limits come from the official documentation: <https://api-docs.deepseek.com/guides/search>.

---

## Install

```bash
make build                 # produces ./bin/deepseek-search
# or
go install github.com/dotSlashLu/deepseek-search-mcp@latest
```

> `go install` names the command after the last element of the module path (`deepseek-search-mcp`); use `make build`, the `deepseek-search` binary from a release archive, or a rename if you prefer the short name. Module paths are case-sensitive: write `dotSlashLu`, an all-lowercase path fails the module-path check.

Requires Go >= 1.25 (imposed by the official MCP Go SDK). `make release` cross-compiles single-file binaries for every supported platform; there are no runtime dependencies.

No Go toolchain? Grab the archive for your platform from [Releases](https://github.com/dotSlashLu/deepseek-search-mcp/releases/latest); it contains the `deepseek-search` binary.

## Install with your agent

Hand this to your agent (Claude Code, Cursor, Codex — any of them); it will pick the package, install and register it:

```text
Install the deepseek-search MCP server from https://github.com/dotSlashLu/deepseek-search-mcp/releases/latest
Put it in ~/.local/bin and register it with my MCP client over stdio, DEEPSEEK_API_KEY=<DEEPSEEK_KEY> (ask me if blank); verify the download against checksums.txt from the same page.
Then confirm tools/list shows deepseek_search and deepseek_fetch, and run a "hello world" search for me.
```

## Usage

### stdio

```bash
DEEPSEEK_API_KEY=sk-xxxx ./bin/deepseek-search
```

Client configuration:

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

Or register it from the Claude Code CLI:

```bash
claude mcp add deepseek-search -e DEEPSEEK_API_KEY=sk-xxxx -- /absolute/path/to/deepseek-search
```

### Streamable HTTP

```bash
DEEPSEEK_API_KEY=sk-xxxx MCP_HTTP_AUTH_TOKEN=my-token \
  ./bin/deepseek-search --transport http --addr 127.0.0.1:8080
```

- MCP endpoint: `http://127.0.0.1:8080/mcp`
- Health check: `http://127.0.0.1:8080/healthz` (no auth)
- Auth: `Authorization: Bearer my-token` or `X-API-Key: my-token`

Client configuration:

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

Without `--http-auth-token` the server still starts (bound to `127.0.0.1` by default) and logs a warning. Pass `--http-auth-required` to refuse to start without a token.

## Configuration

Precedence: **CLI flag > environment variable > default**.

| Flag | Environment variable | Default | Description |
|---|---|---|---|
| `--transport` | `MCP_TRANSPORT` | `stdio` | `stdio` or `http` |
| `--api-key` | `DEEPSEEK_API_KEY` | — | DeepSeek API key, required (except for `--version`) |
| `--base-url` | `DEEPSEEK_BASE_URL` | `https://api.deepseek.com` | API base URL, handy for proxies or gateways |
| `--timeout` | `MCP_TIMEOUT` | `60s` | Timeout per API call, retries included |
| `--max-retries` | `MCP_MAX_RETRIES` | `2` | Retries for 408/429/5xx |
| `--addr` | `MCP_HTTP_ADDR` | `127.0.0.1:8080` | HTTP listen address |
| `--http-path` | `MCP_HTTP_PATH` | `/mcp` | MCP endpoint path |
| `--http-auth-token` | `MCP_HTTP_AUTH_TOKEN` | — | Bearer token for the HTTP transport |
| `--http-auth-required` | `MCP_HTTP_AUTH_REQUIRED` | `false` | Refuse to start HTTP without a token |
| `--stateless` | `MCP_HTTP_STATELESS` | `false` | Serve without MCP sessions (multi-instance deployments) |
| `--http-json-response` | `MCP_HTTP_JSON_RESPONSE` | `false` | Answer with `application/json` instead of an SSE stream |
| `--http-session-timeout` | `MCP_HTTP_SESSION_TIMEOUT` | `30m` | Close idle sessions after this long, `0` to never close |
| `--http-allow-non-local-host` | `MCP_HTTP_ALLOW_NON_LOCAL_HOST` | `false` | Accept requests whose `Host` header is not loopback although the listener is — **required behind a reverse proxy that preserves Host** |
| `--log-level` | `MCP_LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` |
| `--version` | — | — | Print the version |

Logs always go to **stderr** (in stdio mode stdout is the protocol channel).

## MCP tools

### `deepseek_search`

| Parameter | Type | Required | Default | Description |
|---|---|---|---|---|
| `query` | string | ✅ | — | 1–128 characters; supports `site:` `-site:` `-term` `-"phrase"` `"exact phrase"` `lang:` `after:` `before:` |
| `count` | int | | 10 | 1–50 |
| `ori_question` | string | | — | The user's original question, improves query understanding |
| `localization` | string | | — | IP address used to localize results; pass only when the user supplied one |
| `need_content` | bool | | `false` | Also return each page body in the `content` field |
| `max_content_chars` | int | | 4000 | ≥0, `0` means no limit |

Returns readable Markdown (title, URL, site, publication date, authority tier, relevance, snippet, summary, optional body) plus structured metadata. **No results is not an error**: the text says `No results found…`.

### `deepseek_fetch`

| Parameter | Type | Required | Default | Description |
|---|---|---|---|---|
| `url` | string | ✅ | — | 1–8192 characters, absolute HTTP/HTTPS URL, no embedded credentials |
| `max_chars` | int | | 0 | ≥0, `0` means no truncation |

Returns `Title:` / `URL:` followed by the Markdown body. Truncation is marked in the text with `[... truncated at N characters ...]`.

Both tools are annotated read-only and idempotent. Failures come back as tool errors whose message explains the status code, so the model can correct itself.

## Deployment

One binary, no runtime dependencies. A three-part setup works well: **binary + secret file + process supervisor**, with nginx as the public entry point.

### 1. Run it under supervisor (or systemd)

Put the binary anywhere (the example uses `/opt/deepseek-search`) and keep secrets in a 0600 file that the launcher sources, so they never show up in the process list or in the supervisor config:

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

`run.sh` (mode 755, secret file next to it with mode 600):

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

### 2. Expose it through nginx

```nginx
location /mcp/deepseek-search {
    # strip the public prefix; the upstream endpoint is always /mcp
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
    proxy_read_timeout 300s;   # long-lived SSE
    proxy_buffering off;       # required for SSE
    client_max_body_size 8m;
}
```

Optionally expose the health check separately:

```nginx
location = /mcp/deepseek-search/healthz {
    proxy_pass http://127.0.0.1:8080/healthz;
    access_log off;
}
```

### 3. Upgrading

A running binary cannot be overwritten in place (`ETXTBSY`): `supervisorctl stop <program>` → replace the binary → `supervisorctl start <program>`. To rotate keys, edit `config.env` and `supervisorctl restart <program>`.

> Two flags matter for every reverse-proxy deployment: `--http-allow-non-local-host` (otherwise requests carrying the original Host get a 403) and `--http-session-timeout` (otherwise sessions are never reclaimed in a long-running process). Rationale and implementation notes: [docs/engineering.md](docs/engineering.md) (Chinese).

## Billing and rate limits

- Search and Fetch are billed per **successful request**: **$0.75 per 1,000**. Failed requests, empty search results and timeouts are not billed, and `count` does not change the price
- Account-level QPS: 5 for Search, 5 for Fetch, counted independently. Fetch is also subject to a per-domain QPS limit; exceeding either returns 429
- Only HTTP 200 with a result counts as billable; see the official documentation for the status codes

## Development

```bash
make test     # unit tests
make vet      # go vet
make smoke    # end-to-end smoke test over stdio and HTTP (bundled mock API, no real key needed)
make schema   # print the advertised tool descriptions and JSON Schemas
make release  # cross-compile for all platforms
```

Test coverage, design trade-offs and observations about the upstream API live in [docs/engineering.md](docs/engineering.md) (Chinese).

## License

MIT
