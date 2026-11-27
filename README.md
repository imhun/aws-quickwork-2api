# QuickWork / Amazon Quick → OpenAI API

[English](README.md) | [简体中文](README.zh-CN.md)

Expose Amazon Quick (QuickWork) chat through an **OpenAI-compatible HTTP API**.

A single Go executable handles native OAuth / PKCE login, SQLite account storage, token refresh, account rotation, the proxy, and an embedded WebUI. No Python, Node.js, npm, official desktop application, or outpost process is required. The proxy converts `/v1/chat/completions` requests into QuickWork WebSocket frames and returns JSON or SSE responses.

> This is an unofficial project, unaffiliated with Amazon / AWS. Use only accounts you own or are explicitly authorized to access, and comply with applicable terms of service.

## Features

- `POST /v1/chat/completions` with streaming SSE and non-streaming responses.
- `POST /v1/responses` with OpenAI Responses API compatibility, including SSE streaming, non-streaming responses, tool bridging, and multi-turn `function_call_output` context.
- `GET /v1/models` listing configured local model aliases.
- Native social and enterprise OAuth / PKCE login; manual callback submission works on remote servers without an accessible callback port.
- SQLite accounts with round-robin selection, automatic token refresh, and classified retries before streaming begins, including single-account temporary-failure recovery.
- Optional inference API keys, separate from WebUI administrator authentication.
- Embedded English / Simplified Chinese WebUI: overview, account management, browser-based remote login, configuration editing, and a non-streaming Playground.
- Management sessions with HttpOnly cookies, CSRF protection, origin checks, and login rate limiting.
- Multi-stage Docker image and hardened Compose deployment with persistent configuration and accounts.
- WebUI bulk import of HTTP / SOCKS5 proxies, SQLite persistence, and per-account outbound routing.
- Upstream session reuse with pinned accounts and incremental conversation history; explicit `X-Conversation-Id` / `X-Thread-Id` support.
- Client-side tool bridging for `tools`, `functions`, `tool_choice`, and parallel calls. The proxy returns OpenAI `tool_calls`; it never executes tools.
- Upstream usage reporting, including `prompt_tokens_details.cached_tokens`.
- Persistent per-account upstream attempt counts, cumulative attempt time, official token / USD / ACP credit reports, and manual or scheduled official monthly-minute quota sync.
- Oversized user messages use upstream attachments with DLP checks when available, falling back to byte-budgeted middle truncation.
- Optional `chat_agent_id` to select a custom QuickWork agent instead of the default agent's prompt and tool policy.

## Requirements

- Go **1.26.3+** for a native build. Docker builds include the Go toolchain.
- An Amazon Quick account that can sign in normally.
- A browser. Automatic CLI callbacks require it to reach the CLI machine's callback port, normally `18080`; WebUI and `login -manual` do not.

## Quick Start

Download the **v0.5.0** binary for your OS and CPU from [Releases](https://github.com/jasonxu114514/aws-quickwork-2api/releases/tag/v0.5.0), or build from source. Windows, Linux, and macOS binaries are provided for amd64 and arm64. Verify downloads against `SHA256SUMS`; `quickproxy version` reports the embedded version and commit. See [release notes](CHANGELOG.md).

### 1. Build

```bash
go build -o bin/quickproxy.exe ./cmd/quickproxy
```

On macOS / Linux, use `-o bin/quickproxy`. The remaining examples use that executable name; on Windows, use `quickproxy.exe` where needed.

### 2. Sign In to an Upstream Account

```bash
./bin/quickproxy login --provider social
```

First use creates `config.json` and initializes `data/quickwork.db`. Open the discovery URL printed by the command in a browser on the same machine and complete sign-in. The CLI receives callbacks, exchanges tokens, retrieves user information, and stores the account. It reports account information and expiry without printing tokens.

For enterprise SSO:

```bash
./bin/quickproxy login --provider enterprise --region us-east-1
```

Repeat the login command for additional accounts. An existing account with the same email and tenant is updated rather than duplicated. You can also start the server without accounts and add them through the WebUI.

#### Remote / Manual Login

```bash
./bin/quickproxy login -manual
```

1. Open the printed discovery URL in any browser and sign in.
2. When redirected to `http://localhost:18080/callback?...`, copy the **complete address-bar URL** and paste it into the terminal. The callback page may fail to load; that is expected.
3. Open the returned authorize URL and finish authorization.
4. Copy the final `http://127.0.0.1:18080/?code=...&state=...` URL into the terminal.
5. The account is saved to SQLite.

The CLI distinguishes discovery, authorization, and final callbacks by their parameters. It recognizes duplicate discovery callbacks, pasted authorize/discovery URLs, and fragment parameters without repeatedly registering the OAuth client. No callback port needs to be reachable on the server; the port is only part of the generated browser URL. Callback input can also be supplied through stdin.

### 3. Start the Server

Configure the `QUICKPROXY_ADMIN_PASSWORD` environment variable in the server's process environment before starting it. Use a distinct administrator password of **12–1024 bytes**; do not place it in documentation, command history, or `config.json`.

```bash
./bin/quickproxy server
```

The default address is `127.0.0.1:8080`. Open `http://127.0.0.1:8080/` for the WebUI. Without the administrator password, management login is disabled but the inference API remains available. An empty `api_keys` list disables inference authentication and is suitable only for trusted local access.

Override the address for one run without modifying the configuration file:

```bash
./bin/quickproxy server -addr 127.0.0.1:8090
```

### 4. Call the API

```bash
curl http://127.0.0.1:8080/v1/models \
  -H "Authorization: Bearer local"
```

```bash
curl http://127.0.0.1:8080/v1/chat/completions \
  -H "Authorization: Bearer local" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "smart",
    "stream": true,
    "messages": [{"role": "user", "content": "Explain QUIC in three sentences."}]
  }'
```

When `api_keys` is empty, `local` can be any value. If keys are configured, replace it with an allowed inference key. `X-Api-Key` is also supported.

With the OpenAI Python SDK:

```python
from openai import OpenAI

client = OpenAI(base_url="http://127.0.0.1:8080/v1", api_key="local")

for chunk in client.chat.completions.create(
    model="smart",
    messages=[{"role": "user", "content": "hello"}],
    stream=True,
):
    print(chunk.choices[0].delta.content or "", end="")
```

## Docker / Compose

The repository includes a multi-stage `Dockerfile`, `compose.yaml`, and an allowlist-based `.dockerignore`. Only module files, required Go sources, and embedded UI assets enter the build context—not local configuration, databases, or login state.

Set `QUICKPROXY_ADMIN_PASSWORD` in your deployment environment first, then pull and run the published image:

```bash
docker compose pull
docker compose up -d --no-build
```

Compose defaults to `ghcr.io/jasonxu114514/aws-quickwork-2api:v0.5.0`, with Linux amd64 and arm64 images. The registry also provides `0.5.0`, `0.5`, and `latest` tags. Override `QUICKPROXY_IMAGE` to pin another tag or the immutable digest recorded in the release's `container-image.txt`. Docker Hub credentials are not required for public GHCR pulls.

To build the checked-out source instead, run `docker compose up -d --build`. Local builds use `QUICKPROXY_VERSION` for version metadata (default `v0.5.0`); this does not choose a Git revision. For standalone builds, use `docker build --build-arg VERSION=v0.5.0 -t quickwork-openai:local .`.

Open `http://127.0.0.1:8080/`, sign in with the administrator password, and add upstream accounts from the Accounts page. `QUICKPROXY_PORT` changes the published host port; the container still listens on `0.0.0.0:8080`. Compose publishes to the host's loopback interface by default.

Deployment details:

- Named volume `quickproxy-data` mounts at `/app/data`; first use creates `/app/data/config.json`.
- Relative database paths resolve against the configuration directory. The default database is therefore **`/app/data/data/quickwork.db`**. Set `database` to `quickwork.db` and restart to use `/app/data/quickwork.db` instead.
- Existing configuration and account data survive image rebuilds and restarts. Do not remove the volume during upgrades.
- Back up the configuration and SQLite database before upgrading; schema changes are applied automatically at startup. Proxy inventory and bindings share the same database and volume.
- The process runs as UID/GID `10001:10001`. A bind-mounted data directory must already be writable by this user.
- Compose uses a read-only root filesystem, drops all Linux capabilities, enables `no-new-privileges`, and provides a temporary `/tmp`.
- The health check calls only `/healthz` inside the container; it does not contact the upstream service or require accounts.
- There is no default inference API key. Configure `api_keys` before permitting remote access, and use a TLS reverse proxy with access controls rather than directly exposing the management interface.

Optional manual CLI login inside the running container:

```bash
docker compose exec quickproxy /app/quickproxy login -manual -config /app/data/config.json
docker compose exec quickproxy /app/quickproxy accounts -config /app/data/config.json
```

Configuration saved through the WebUI takes effect after a restart:

```bash
docker compose restart quickproxy
```

To build the image separately:

```bash
docker build -t quickwork-openai:local .
```

### CI and Releases

The `ci` workflow compiles Go, runs `go vet`, checks formatting of changed Go sources and JavaScript syntax, and builds both Linux container architectures on native GitHub runners. It does not run the application, probe proxy endpoints, or perform smoke tests.

Pushing a stable `vMAJOR.MINOR.PATCH` tag starts `release`. Manual dispatch requires an existing tag in the `tag` input. The workflow pins the tag's commit for every build, produces six binaries and multi-platform GHCR images, and publishes the GitHub Release after all distribution builds succeed. The registry path is derived from the repository name; publication uses the workflow's `GITHUB_TOKEN` with job-scoped `packages: write`, without a separate registry secret. All third-party Actions are pinned to commit SHAs. A failed run can be rerun for the same tag without moving it.

## WebUI

The embedded WebUI uses the same listener as the inference API. Set `webui.enabled` to `false` and restart to disable both the UI and management API without disabling inference.

### Authentication and Languages

- The administrator password comes only from `QUICKPROXY_ADMIN_PASSWORD`; it is not stored in configuration and is independent of inference API keys.
- Management sessions use HttpOnly, SameSite=Strict cookies. Mutations require CSRF validation and origin checks; login attempts are rate-limited.
- `webui.session_ttl` defaults to `12h`. Restarting the service invalidates sessions.
- The language selector switches between **English** and **简体中文**. UI language does not change account settings, models, or the upstream `locale` configuration.
- Management endpoints use unversioned `/api/` paths and do not inherit the inference API's wildcard CORS policy. Inference API keys do not grant management access.

### Pages

- **Overview:** process start time, models, account availability, real upstream call outcomes, cumulative attempt time, reported tokens, USD costs, and ACP credits grouped by unit. Legacy selection counts remain separate.
- **Accounts:** per-account call / duration / token / cost / credit statistics and official monthly-minute quotas, alongside non-secret account information, enable/disable rotation, token refresh, and confirmed local deletion. Quota synchronization is separate from token refresh. Deletion does not revoke upstream authorization.
- **Add Account:** the existing two-stage OAuth / PKCE flow, using pasted browser callbacks without requiring server or container callback-port access.
- **Configuration:** edit JSON and save it to disk. The running configuration is not hot-reloaded; restart the service to apply changes. Inference keys are never returned to the browser. Leave the replacement field empty to preserve existing keys; explicitly choose clear to remove all keys.
- **Playground:** non-streaming chat requests. Supply an inference API key separately if configured; the administrator password cannot authenticate inference. Secrets are not persisted in browser storage.

### Add an Account in the Browser

1. Choose a provider and build stage on the Accounts page; enter a region and select an outbound proxy when needed.
2. Start login, open the discovery link, and sign in.
3. Copy the complete `localhost` callback URL from the browser address bar and submit it to the WebUI. An unreachable callback page is expected on a remote machine.
4. Open the returned authorize link, finish authorization, and submit the final complete callback URL containing `code` and `state`.
5. The account is stored in SQLite and the list updates. Cancel an unfinished flow or restart it after expiry.

Callback URLs contain temporary authorization data: do not share them in chat, logs, or issue reports. Account tokens, refresh tokens, and client secrets are not returned by the management API.

### Outbound Proxies

On **Accounts → Outbound proxies**, paste one proxy URL per line and choose **Import proxies**. Entries are stored in the configured SQLite database, not a proxy file. Supported formats include:

```text
http://proxy.example.com:8080
http://USER:PASS@proxy.example.com:8080
socks5://proxy.example.com:1080
socks5://USER:PASS@proxy.example.com:1080
```

Percent-encode special characters in usernames and passwords, such as `%40` for `@`. Empty lines and lines starting with `#` are ignored. Imports are atomic: invalid entries reject the entire batch; canonical duplicates are skipped. Each import accepts up to 1,000 distinct proxies, with a 1 MB JSON request limit.

- Select a stored proxy on each account row, or select it before starting browser OAuth. Login requests, token refresh, user information, official usage, upstream WebSocket connections, attachment uploads, DLP polling, and attachment events use that route. The browser's own navigation still uses the browser's network configuration.
- **Inherit default / environment** uses the optional `config.json` `proxy_id`. If that is empty, existing process environment proxy behavior is preserved. **Direct** (`"direct"`) bypasses environment proxies. Explicit account bindings override the global default; unavailable bindings fail rather than silently falling back.
- Imports and account binding changes apply to subsequent outbound requests without restarting. Requests already in flight keep their route. The full proxy ID is displayed beneath its sanitized address; copy it into `proxy_id` in Settings to select a global default, then save and restart.
- Reauthentication without an explicit proxy selection preserves an existing account binding. To clear it, choose **Inherit** on the account row. Unbind accounts and remove any running or saved default reference before deleting a proxy.
- Credentials are stored **unencrypted in SQLite**, alongside account credentials. Protect the database and its backups, and use HTTPS when accessing the WebUI remotely. Listings return only IDs and addresses without user information; they never return stored proxy credentials. The import textarea is cleared on submission.

Proxy management endpoints use the same management session, origin checks, and CSRF protection as other management writes:

| Endpoint | Purpose |
|---|---|
| `GET /api/proxies` | List `{proxies: [{id, label}]}` without credentials |
| `POST /api/proxies/import` | Import `{"proxies":"http://proxy.example.com:8080\nsocks5://proxy.example.com:1080"}`; returns `{imported: N}` |
| `DELETE /api/proxies/{id}` | Delete an unbound proxy; returns `409 proxy_in_use` while referenced |
| `PATCH /api/accounts/{id}` | Set `{"proxy_id":"<stored ID>"}`, `{"proxy_id":"direct"}`, or `{"proxy_id":""}` |
| `POST /api/logins` | Optional `proxy_id` chooses the login route and binds the resulting account |

### Account Statistics and Official Usage

The Accounts page distinguishes local attempt statistics from separately synchronized official allowances:

- **Calls:** each real `Upstream.Chat` attempt counts once, including failed attempts and retries. A successful attempt returns visible text or valid tool calls. Legacy `use_count` records account selections, not actual calls.
- **Time:** `total_duration_ms` sums each attempt's wall-clock time. Token refresh, retry backoff, and other non-chat work are excluded. It is not an account's lifetime or logged-in duration.
- **Tokens:** input, output, total, cache-read, and cache-write counts come only from official WebSocket `turn_usage`. Unreported usage is not estimated. Statistics begin at `stats_since`; history before this feature is not reconstructed.
- **USD:** `cost_usd` uses official `turn_usage` cost reports only. It may be unknown or cover only some calls; `cost_reported_calls` shows that coverage. A numeric zero means an explicit recorded zero, not an unknown cost.
- **Credits:** official `acp_metadata` provides `cost_value` and `cost_unit_plural` (default unit: `credits`). Assistant-message snapshots replace earlier reports for that message instead of double-counting repeated frames. Totals are accumulated separately for each unit, with `reported_calls` coverage; different units are never added together. Ordinary `smart` / `fast` traffic may not send ACP metadata. An empty credits list means **Credits not reported**, not zero. Reported usage from failed or retried attempts is still counted.
- **Monthly quota:** the official profile / usage source reports **minutes**, separately for the normal monthly allowance and monthly overage. Used, limit, and remaining values can be unknown. A missing overage limit must not be treated as zero; the plan may be uncapped. Remaining minutes are derived only when valid used and limit values exist, never from token counts or USD.

**Credits, USD, tokens, and monthly minutes are different measurements. The proxy does not convert between them or infer a credit balance from local calls.**

Use **Sync usage** for one account or **Sync all usage** for the enabled-account batch. A sync updates the official quota snapshot; it does not change recorded chat tokens, costs, or credits. The UI displays the last successful sync, latest attempt, reset time, and `unknown` / `available` / `stale` / `error` / `unsupported` status. Failed synchronization keeps the previous successful snapshot when one exists and marks it stale, rather than replacing it with zero.

Automatic synchronization runs at startup and then every `usage.refresh_interval` for enabled accounts, defaulting to five minutes. `usage.enabled=false` disables only automatic synchronization; manual actions remain available. `usage.max_attempts` limits retries for temporary official-usage failures. The Accounts page shows the effective scheduler settings and next run; edit `usage` in Settings, save, and restart to apply changes.

The official profile transport is implemented from confirmed source behavior, but **has not been validated with a live request in this change**. Providers and subscription plans may omit or reject these endpoints. Unavailable / unsupported responses remain unknown; no quota or credit values are fabricated.

Management endpoints require the management session and CSRF header for writes:

| Endpoint | Purpose |
|---|---|
| `GET /api/accounts` | Account details with `usage` statistics and cached `quota` snapshots |
| `GET /api/status` | Aggregate calls / time / reported usage and effective `usage_sync` scheduler state |
| `POST /api/accounts/{id}/usage/refresh` | Synchronize one account's official quota; returns `{quota: ...}` |
| `POST /api/accounts/usage/refresh` | Synchronize enabled accounts; returns per-account results |

Individual sync errors use `usage_unavailable`, `usage_unauthorized`, `usage_failed`, or `usage_busy`; the table reloads after success or failure so retained / stale snapshots stay visible. These endpoints do not expose account tokens or raw upstream response bodies.

## Configuration

Commands create `config.json` with defaults if it does not exist. Relative database paths are resolved against its directory.

```json
{
  "addr": "127.0.0.1:8080",
  "database": "data/quickwork.db",
  "proxy_id": "",
  "api_keys": [],
  "models": ["smart", "fast", "balanced", "auto"],
  "default_model": "smart",
  "request_timeout": "5m",
  "refresh_before": "10m",
  "max_attempts": 3,
  "app_version": "1.2.4660",
  "surface": "QUICK_WORK_DESKTOP_WINDOWS",
  "outpost_id": "",
  "working_session_id": "",
  "timezone": "UTC",
  "locale": "en-US",
  "thinking_effort": "",
  "chat_agent_id": "",
  "keep_sessions": false,
  "session_reuse": true,
  "session_ttl": "30m",
  "webui": {
    "enabled": true,
    "session_ttl": "12h"
  },
  "usage": {
    "enabled": true,
    "refresh_interval": "5m",
    "max_attempts": 3
  },
  "oauth": {
    "provider": "social",
    "build_stage": "prod",
    "region": "",
    "callback_port": 18080,
    "timeout": "1h"
  }
}
```

| Field | Default | Meaning |
|---|---|---|
| `addr` | `127.0.0.1:8080` | Shared API / WebUI listener |
| `database` | `data/quickwork.db` | SQLite path, relative to the configuration directory |
| `proxy_id` | Empty | Global default: stored proxy ID or `direct`; empty preserves process environment proxy behavior; restart after editing |
| `api_keys` | `[]` | Allowed inference keys; empty disables inference authentication |
| `models` | `smart,fast,balanced,auto` | Local model aliases returned by `/v1/models` |
| `default_model` | `smart` | Fallback mapping for unknown model names |
| `request_timeout` | `5m` | Timeout for an upstream request |
| `refresh_before` | `10m` | Refresh a token when its remaining lifetime is below this threshold |
| `max_attempts` | `3` | Maximum real upstream `Chat` attempts across all retries per request, not distinct accounts |
| `app_version` | `1.2.4660` | Upstream handshake application version |
| `surface` | `QUICK_WORK_DESKTOP_WINDOWS` | Upstream handshake surface type |
| `outpost_id` / `working_session_id` | Empty | Optional upstream handshake fields |
| `timezone` / `locale` | `UTC` / `en-US` | Timezone and language sent in `user_message`; independent of UI language |
| `neutral_prompt` | `false` | Neutral mode: replace caller system text with a short neutral prompt, rewrite Codex AGENTS.md / environment_context envelopes as neutral project and environment notes, use `<available_tools>` with compacted schemas, and limit the tool surface |
| `neutral_tool_limit` | `16` | Maximum number of tools kept in neutral mode, chosen by built-in priority |
| `thinking_effort` | Empty | `off`, `low`, `medium`, or `high`; empty uses upstream defaults |
| `chat_agent_id` | Empty | Custom QuickWork agent with its own system prompt and tool policy |
| `keep_sessions` | `false` | Keep upstream sessions when session reuse is disabled |
| `session_reuse` | `true` | Reuse a pinned upstream thread and send only new history |
| `session_ttl` | `30m` | Idle expiry of in-memory conversation state |
| `webui.enabled` | `true` | Enable embedded UI and management endpoints; restart required |
| `webui.session_ttl` | `12h` | Management session lifetime, separate from conversation TTL |
| `usage.enabled` | `true` | Synchronize official usage for enabled accounts at startup and periodically; manual sync remains available when disabled |
| `usage.refresh_interval` | `5m` | Official usage sync interval, from `1m` to `24h` |
| `usage.max_attempts` | `3` | Attempts for temporary official-usage failures, from `1` to `5`; separate from chat `max_attempts` |
| `oauth.provider` | `social` | Default login provider |
| `oauth.build_stage` | `prod` | `prod`, `beta`, `integ`, or `dev` |
| `oauth.region` | Empty | Enterprise discovery region prefix; social uses the discovered IDC region |
| `oauth.callback_port` | `18080` | Local OAuth callback port |
| `oauth.timeout` | `1h` | Maximum time to complete browser login |

Durations accept Go duration strings such as `30s`, `5m`, and `1h`. `server -addr ADDRESS` overrides only the running listener and does not write to the configuration file. Docker uses this flag by default, so its command-line listener still wins after changing `addr` in the WebUI. Configuration saved in the WebUI takes effect only after restarting the process.

Configure inference keys in `api_keys`, then send an allowed key using `Authorization: Bearer ...` or `X-Api-Key`. Never commit real keys or account data.

## Commands

| Command | Purpose |
|---|---|
| `quickproxy server` | Start the API and enabled WebUI |
| `quickproxy server -addr ADDRESS` | Override the listener for this run |
| `quickproxy login` | Browser OAuth login and SQLite account storage |
| `quickproxy accounts` | List accounts, expiry, selection counts, and recent errors |
| `quickproxy refresh` | Refresh every account |
| `quickproxy refresh -account ID` | Refresh one account |
| `quickproxy logout -all` | Delete all local accounts |
| `quickproxy logout -account ID` | Delete one local account |
| `quickproxy version` | Show the build version and source commit without loading configuration |

All commands accept `-config PATH`, defaulting to `config.json`. Running without a subcommand starts the server.

| Login Flag | Default | Purpose |
|---|---|---|
| `-provider` | `config.oauth.provider` | `social`, `enterprise`, `gateway`, or `social_gateway` |
| `-stage` | `config.oauth.build_stage` | `prod`, `beta`, `integ`, or `dev` |
| `-region` | `config.oauth.region` | Enterprise discovery region prefix or social IDC region |
| `-port` | `config.oauth.callback_port` | Local callback port |
| `-timeout` | `config.oauth.timeout` | Login timeout |
| `-open` | Off | Also open the URL in the default browser |
| `-manual` | Off | Paste callback URLs instead of opening a callback listener |
| `-proxy-id` | Empty | Stored proxy ID or `direct`; empty inherits the global default / environment; explicit selection binds the account |

Upgrading from the old Python login flow: `auth/quickauth.py` was removed; `data/session.json` and `data/upstream.json` are no longer read. Sign in again using `quickproxy login`; SQLite is now the account source of truth.

## How It Works

Native Go OAuth exchanges credentials and saves accounts in SQLite. The proxy selects an enabled account, refreshes expiring credentials, then connects to the upstream WebSocket. `internal/proxy/wire.go` handles wire encoding and event-stream decoding; the upstream is not an OpenAI HTTP endpoint.

### Account Rotation and Retries

Accounts are selected round-robin in ID order. Failed refreshes or unusable accounts record `last_error` and are skipped. `max_attempts` is the total budget of actual upstream `Chat` attempts for a request, not a count of distinct accounts; selection failures and token refreshes do not consume a chat attempt. Retries share the original request deadline and use exponential backoff starting at 200 ms, capped at two seconds.

- **Temporary failures:** upstream throttling / HTTP 429, timeouts / HTTP 408, HTTP 5xx, selected transient network and WebSocket disconnects, and empty visible output can retry. Untried eligible accounts are preferred; once they are exhausted, a still-eligible account may be retried, so a single-account setup can recover within the same attempt budget.
- **Authentication:** expired / invalid credentials trigger at most one explicit token refresh per account in a request, then retry that account if refresh succeeds. A repeated authentication failure excludes that account for the rest of the request.
- **Permission or quota:** forbidden / payment-required / credit-exhausted / quota errors skip that account and try another eligible account; they do not repeatedly retry the exhausted account.
- **Reused session missing:** invalidate local session state and retry with the full transcript in a new upstream session; an unrecoverable new-session error skips the account.
- **No retry:** client cancellation, oversized or invalid requests, and other permanent errors stop immediately. The request deadline always wins over retries.
- **Streaming boundary:** once the first SSE response byte is sent, no retry or account switch is attempted, avoiding duplicate output. Non-streaming and tool-buffered responses may retry only before output is committed.

Every real attempt contributes to the account's call count, duration, and any official usage it reported, even if the attempt fails and another succeeds. Retries can therefore incur additional upstream usage or billing. Missing token / USD / ACP reports remain unknown or partial; successful delivery does not imply that previous attempts were free. Rotation is request-based, not quota- or token-based. An invalid refresh token requires signing in again.

### Prompts and Tool Calls

- `system` / `developer` messages and tool definitions are combined in order at the beginning of the current user text, separated from the task by a `---` rule. No `[System instructions ...]` role labels are added, because that phrasing is treated as prompt injection upstream. The upstream ignores `user_message.payload.system_prompt` and treats `custom_prompt` as lower-trust preferences, so these fields are not used for caller instructions.
- Requests over the 64 KB frame limit are split across frames. Frame headers carry only a neutral pagination marker (`part i of n`); earlier parts are never described as system instructions, and the tool block is appended only to the final frame next to the actual task.
- `neutral_prompt=true` swaps the caller system text for a neutral engineering prompt, switches the tool block to `<available_tools>`, compacts schemas, and keeps at most `neutral_tool_limit` tools. The default upstream identity, system context, and built-in tools remain server-controlled.
- The default QuickWork agent owns a product identity and its own tools. Any user-turn text that introduces a different tool protocol or asks it to act as an external coding CLI is classified as prompt injection; rewording only lowers the probability and cannot remove the boundary.
- Codex also sends `# AGENTS.md instructions` and `<environment_context>` as user messages, which the model reads as another agent's system instructions. With `neutral_prompt=true` the proxy removes the envelope wording and re-emits the repository rules and working directory as neutral project and environment notes, so coding context survives. Verified with a short turn and a 700 KB / 13-frame turn, both invoking the client tool.
- Full isolation requires making the tool protocol first-party: create a QuickWork custom agent whose system prompt declares the bridge, then set `chat_agent_id` (see the next section). Alternatively use a raw model endpoint without the product-agent guardrails.
- Tools use `<caller_tool_interface>` definitions, or `<available_tools>` when `neutral_prompt=true`, and `<client_tool_call>{"name":"...","arguments":{...}}</client_tool_call>` output.
- Responses API `type:"custom"` (freeform) tools are bridged too: the proxy synthesizes an `{input:string}` parameter, returns `custom_tool_call` with the raw `input`, and parses `custom_tool_call` / `custom_tool_call_output` from history. Codex's `apply_patch` uses this path.
- Codex only declares `apply_patch` when the model catalog marks the model as `"apply_patch_tool_type": "freeform"`.
- The model catalog must also set `"supports_search_tool": false`. With `true`, Codex uses tool-search mode and sends namespaces without child tools, so MCP tools such as patchloom / zvec / fff never reach the bridge. Point `model_catalog_json` at a catalog containing the slug, e.g. `{"models":[{"slug":"aws-quick/smart","apply_patch_tool_type":"freeform","context_window":1000000}]}`. Parsing also accepts alternate tool tags, JSON wrappers, Markdown fences, `arguments` / `parameters` / `input` / `args`, namespace prefixes such as `functions.`, case variations, and unique suffix matches. Unknown tools or invalid JSON remain ordinary text.
- Multiple calls can be returned in one turn. `parallel_tool_calls=false` limits output to one call; `tool_choice: "none"` disables bridging, and a forced function only accepts that function.
- The proxy returns OpenAI `tool_calls` but does not execute them. Clients execute tools and send `role: tool` results back; historical calls and results are rendered into upstream text context.
- Native upstream tool events are also collected as a fallback when they match declared client tools. Multiple event-stream messages in one WebSocket frame are all processed.
- Tool-enabled streaming is buffered until the turn ends. Thinking-only, whitespace-only, or zero-width-only output returns an explicit error rather than an empty successful completion.
- The default built-in agent can add roughly 60K+ tokens of upstream prompt context. To replace its prompt and tool policy, configure `chat_agent_id` to reference a custom QuickWork agent; caller instructions alone cannot remove the default identity.

### Creating a Custom Agent

A custom agent moves the tool-bridge protocol into the upstream agent's own system prompt instead of leaving it as unfamiliar text in the user turn. In testing this is the most reliable way to stop refusals such as "this is prompt injection" or "I will not act as an external coding CLI".

Create it through the QuickWork data plane: `POST {tenant_url}/integration/quick-work/qbs-event` with the account `access_token` as `Authorization: Bearer` and `Origin: https://quick.aws.com`. **Field names must be camelCase**; PascalCase silently returns an empty `eventResponse` without an error.

```json
{
  "eventType": "createAgent",
  "eventBody": {
    "createAgent": {
      "agentId": "codex-bridge",
      "name": "Codex Bridge",
      "description": "Local coding tool bridge for Codex",
      "agentLifecycle": "PUBLISHED",
      "customPromptInput": {
        "newPrompt": {
          "customInstructions": "## Tool channel\nEach user message may end with an <available_tools> block..."
        }
      }
    }
  }
}
```

`customInstructions` should at least state that:

1. `<available_tools>` / `<caller_tool_interface>` is the tool list injected by the local runtime for this session, and is to be used directly without questioning or restating it;
2. a call is exactly `<client_tool_call>{"name":"TOOL","arguments":{...}}</client_tool_call>`;
3. markers must not be wrapped in Markdown fences and must not be surrounded by prose;
4. the tools really read and write local files and run commands, so the model must not answer "I cannot see your repository" or "I cannot run commands".

After creation, `describeAgent` should report `agentStatus=ACTIVE` and a non-empty `customPromptInterface.customInstructions`. To change the prompt later, call `updateAgent` with both `internalAgentId` and `version`; omitting either returns HTTP 400:

```json
{
  "eventType": "updateAgent",
  "eventBody": {
    "updateAgent": {
      "agentId": "codex-bridge",
      "internalAgentId": "PUBLISHED-…",
      "version": 2,
      "name": "Codex Bridge",
      "customPromptInput": { "newPrompt": { "customInstructions": "…" } }
    }
  }
}
```

Then set `"chat_agent_id": "codex-bridge"` in `config.json` and restart. Keep `neutral_prompt=true`: forwarding Codex's full system prompt alongside the bridge protocol conflicts with it, and models then tend to answer that they cannot call the tools.

### Session Reuse

The first request sends the full transcript and records its thread/account. If later history extends the recorded history, only the suffix is sent. The conversation key uses `X-Conversation-Id`, then `X-Thread-Id`, otherwise a hash of the full system/tool prompt plus the first non-system message. Prompt/tool changes, edited history, or no new messages start a fresh upstream session. Conversations pin the original account; unavailable accounts or failed reuse fall back to a new account and full transcript. State is memory-only, expires after `session_ttl`, and is lost on restart. Reused upstream sessions are not explicitly deleted. With reuse disabled and `keep_sessions=false`, sessions are deleted after each turn.

### Request Logs

Chat logs report request start, account attempts, upstream event counts, and final outcome: model, attempts, elapsed time, token usage, finish reason, tool-call count, and errors. Status is `ok`, `empty`, or `failed`. Logs do not intentionally include tokens, client secrets, or request bodies, but can include account email addresses and upstream errors; protect access to them.

## Security and Privacy

- The SQLite database contains access/refresh tokens, client secrets, and user information. Runtime data is gitignored; do not commit or share `data/` or persistent volumes.
- `config.json` has no keys by default. If you configure inference keys, treat the file as sensitive.
- Upstream passwords are entered only on Amazon's browser pages, not stored by this project. Use a separate WebUI administrator password rather than reusing an upstream password.
- Management configuration/account responses do not expose inference keys, account tokens, refresh tokens, or client secrets. Treat temporary OAuth callback URLs as sensitive.
- Oversized user text can be uploaded into your Quick tenant as an attachment and is subject to your organization's DLP policy.
- The native server and Compose host publishing default to loopback. For remote use, add TLS and access controls, configure inference authentication, and restrict management access.
- `quickproxy logout -all` deletes local accounts and their refresh state; it does not revoke upstream grants.

## Limitations

- Chat completions only. Sampling fields such as `temperature`, `max_tokens`, `stop`, and `response_format` are not forwarded; `n > 1` returns HTTP 400.
- Each upstream `user_message` has a 64 KB JSON-byte limit. Reuse reduces history size. Oversized new user text first attempts attachment upload, DLP polling, and delivery through `files/upload/<thread>`. Only DLP `ALLOW` is accepted; `WARN` is unavailable. Caller instructions and tool definitions stay in WebSocket text rather than attachments.
- Missing tenant URLs, gateway transport, upload failures, or rejected DLP checks fall back to truncating the middle while preserving the beginning and end within the byte budget. Only a minimal frame that still cannot fit produces `400 request_too_large`.
- WebSocket fragmentation does not bypass the reassembled message limit. Multiple user messages are multiple turns; `mid_turn` steering and `message_split` output events cannot reassemble a single input turn.
- Text-based tool bridging depends on model behavior and can fail to follow the protocol. Tool-enabled streaming is buffered, and tools remain client-executed.
- The default upstream prompt, product identity, and built-in tools cannot be removed by per-turn caller text. Use a custom `chat_agent_id` for isolation.
- Models map to upstream modes `smart`, `fast`, `balanced`, and `auto`. `/v1/models` lists local aliases, not a discovered upstream model catalog.
- Social / enterprise paths have previously been exercised with real accounts; `gateway` / `social_gateway` CBOR transport remains experimental. Caller-instruction behavior on enterprise / gateway still needs validation.
- Upstream access tokens usually expire after about an hour. Refresh uses `refresh_before`; omitted replacement ID tokens preserve the previous value, and transient user-info failures do not discard successfully refreshed tokens.
- Configuration edits are restart-only. Management sessions and conversation reuse state are not persistent.
- Recorded account statistics do not reconstruct earlier history. Token / USD / ACP credit reports may be absent or partial, and official monthly-minute quota support varies by provider and plan; the quota transport is source-confirmed but not live-validated in this change.
- Upstream interfaces can change with official client updates. Re-login first when authentication or chat stops working.
- No association with Amazon / AWS and no service guarantees.

## Troubleshooting

| Symptom | Action |
|---|---|
| `cannot listen on 127.0.0.1:18080` | Free the callback port or change `-port` / `oauth.callback_port` |
| CLI keeps waiting after browser login | Use the same machine with a reachable callback, or use `login -manual` / WebUI |
| Remote `localhost:18080` callback does not load | Expected in manual mode; copy the complete address-bar URL |
| `upstream_session_unavailable` / `no accounts configured` | Add an account in the WebUI or run `quickproxy login` |
| `401 invalid_api_key` | Check inference keys against the request header; administrator credentials are separate |
| WebUI says no administrator password is configured | Set `QUICKPROXY_ADMIN_PASSWORD` to 12–1024 bytes in the server environment and restart |
| Playground cannot authenticate | Enter a configured inference API key in Playground, not the administrator password |
| Saved settings do not change behavior | Restart the server; `server -addr` still overrides the listener |
| Account attempts keep failing | Inspect `quickproxy accounts`; refresh or sign in again for the affected account |
| Tokens expire | Automatic refresh normally handles this; use `quickproxy refresh` if needed |
| Social login fails at user information with `net/http: TLS handshake timeout` | See the process-scoped TLS compatibility workaround below; keep default TLS settings unless affected |
| Container account data appears missing | Use the same volume and `/app/data/config.json`; default DB is `/app/data/data/quickwork.db` |
| Model refuses with "I can't do that" / "another tool protocol" / "part N" | Enable `neutral_prompt`; the proxy rewrites Codex AGENTS.md and environment_context envelopes into neutral project and environment notes. If it persists, the default agent guardrail cannot be bypassed: use a custom `chat_agent_id` agent or a raw model endpoint |

### TLS Handshake Compatibility

Issue #10 reports that social login failed while fetching user information on macOS arm64 with Go 1.26.11. An independent Go HTTP client also timed out on the same host; disabling hybrid ML-KEM key exchange for that process restored connectivity. This does not establish whether the cause is the AWS endpoint or an intermediary, and it is not a universal fix for TLS timeouts.

If affected, temporarily run both login and the server with this process-scoped setting (POSIX shell):

```sh
GODEBUG=tlsmlkem=0 ./bin/quickproxy login --provider social -open
GODEBUG=tlsmlkem=0 ./bin/quickproxy server
```

In PowerShell, use a child process so the setting does not persist in the current session:

```powershell
cmd /c "set GODEBUG=tlsmlkem=0&& bin\quickproxy.exe login --provider social -open"
cmd /c "set GODEBUG=tlsmlkem=0&& bin\quickproxy.exe server"
```

The project keeps Go's default TLS behavior. This opt-in workaround changes key exchange only; certificate verification remains enabled. If you already use other `GODEBUG` options, retain them in the comma-separated value. Remove the workaround when it is no longer needed. See [Go's TLS compatibility notes](https://go.dev/doc/go1.24#crypto/tls).

## Layout

```text
quickwork-openai/
  cmd/quickproxy/           CLI commands and server startup
  internal/admin/          Embedded WebUI, administrator sessions, management API
  internal/admin/static/   HTML / CSS / JS and English / Chinese UI
  internal/config/         Configuration loading, defaults, persistence
  internal/oauth/          Native OAuth / PKCE, token refresh, user information
  internal/oauth/browser.go Staged callback flow shared with the WebUI
  internal/store/          SQLite accounts and proxy inventory / bindings
  internal/outbound/       Shared HTTP / SOCKS5 outbound routing
  internal/accounts/       Account rotation and automatic refresh
  internal/proxy/          OpenAI handlers, WebSocket protocol, sessions, tools
  config.json              Generated server configuration
  data/quickwork.db         Runtime account database, gitignored
  Dockerfile               Multi-stage image, non-root runtime
  compose.yaml             Persistent volume and loopback publishing
  README.md                English documentation
  README.zh-CN.md           Simplified Chinese documentation
```

## License

Licensed under the [GNU General Public License v3.0](LICENSE).

## Acknowledgements

Thanks to the [LINUX DO](https://linux.do) community for its support.
