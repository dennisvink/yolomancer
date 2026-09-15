# Yolomancer agent API

The API is a standalone, long-running process. It uses the same agent engine,
ADK/Bedrock transport and tools as the CLI, without a terminal or permission
dialogues. Starting or exiting the interactive CLI does not start or stop it.

## Start and stop

```sh
yolomancer serve --config agents.yaml --check
yolomancer serve --config agents.yaml
```

The default listener is `127.0.0.1:8081`. Ctrl+C or SIGTERM stops the server and
cancels its active runs. An occupied port or invalid configuration causes startup
to fail without opening another port. Configuration is fixed for the lifetime
of the server; restart after editing it. Python implementations are loaded at
execution time, so restart after changing their metadata/schema as well.

Options: `--config PATH`, `--host IP`, `--port PORT`, `--profile AWS_PROFILE`,
`--check`. `--check` validates configuration and tool metadata but does not call
a model, verify AWS permissions or test extension dependencies. No API service
starts automatically just because you start the interactive CLI.

## Provisioned labs

New workspace images include an API supervisor alongside the website supervisor.
After source build, Python dependency installation and credential registration,
it runs `/workspace/yolomancer serve --config /workspace/agents.yaml` as
`participant`, using the same Python venv and registered credentials as the CLI.
The supplied configuration binds only to loopback; no public 8081 listener is
created and the users.yolomancer.com proxy cannot connect to it over the task IP.

```sh
lab-api stop
lab-api start
lab-api restart
```

These controls take effect on the supervisor's next tick. Stop remains in effect
until start/restart; a crashed service is retried. A missing/invalid `agents.yaml`
leaves the API off and is checked again every five seconds without breaking SSH
or the website. Inspect validation errors with `yolomancer serve --check`.
Exiting your SSH session does not stop the service. The existing lab lifetime
still applies; this is not persistence beyond workspace termination.

The lab supervisor optionally reads `~/.yolomancer/api.env` on service startup.
Use literal `NAME=value` lines (no quotes, shell substitution or `export`), and
protect it with `chmod 600`. Restart the API after changing it. For example:

```text
YOLOMANCER_API_TOKEN=replace-with-a-strong-random-token-of-at-least-32-characters
```

Existing images/workspaces need the updated source and supervisor/image; merely
updating a local file does not change already deployed containers. Existing
workshop repositories without `agents.yaml` must add it before the API starts.

## Agent configuration

YAML and JSON are accepted with the same field names. Unknown fields, duplicate
YAML keys, multiple documents, unknown tools, invalid modes and missing configured
token variables fail validation. Paths are relative to the configuration file,
not the caller's directory. Workspaces, extension files and restricted roots must
exist. The server never accepts tools, credentials, paths or permission overrides
from an HTTP caller.

The repository's `agents.yaml` defines a deliberately permissive `workshop`
agent: shell commands, process polling, file tools, AWS CLI, image metadata,
case-law search and the board extension. It runs with the participant's OS/AWS
privileges, including the lab's sudo access. Treat access to this agent as access
to the lab account, not as a harmless chat endpoint.

```yaml
version: 1
server:
  host: 127.0.0.1
  port: 8081
  max_concurrent: 1
agents:
  reader:
    workspace: .
    tools:
      builtin: [read_file, list_files]
      python: []
    permissions:
      mode: restricted
      approval: deny
      read_roots: [documentation]
      write_roots: []
      network: deny
    limits:
      timeout_seconds: 300
      token_budget: 100000
```

Use `mode: yolo`, `approval: deny`, `network: allow` for trusted agents needing
shell, AWS CLI or Python extensions. Yolo cannot be combined with root restrictions
or network denial. `restricted` supports file tools only and denies access outside
the listed roots, including symlinks resolved outside them. Empty root lists grant
no file access. `network` describes tool access, not the model's Bedrock connection.

Arbitrary shell/Python code is **not** sandboxed by file-tool path checks. These
tools therefore require yolo rather than pretending a restricted policy can
contain them. The allowlist limits direct agent tool calls, not what an allowed
shell or Python program can implement itself. Use a separate container and narrow
IAM role for untrusted code. A worker process is state isolation, not a security
boundary against another process running as the same OS user.

Available builtins: `exec_command`, `write_stdin`, `read_file`, `write_file`,
`replace_in_file`, `list_files`, `aws_cli` (requires `aws` in PATH).
Python extensions are explicit file paths exporting `yolomancer_tool()` and
`run(args)`. Metadata may reference literal module constants but not imports or
dynamically computed module globals. Python and requirements must be installed in
the service environment; the API does not install arbitrary dependencies.
Unlisted extensions are not auto-discovered. Names must be unique and must not
shadow builtins. Goal-management tools are CLI-only: an API run is one bounded
request, not a persistent `/goal` continuation.

Defaults: 600 seconds/run, 200,000 accounted tokens/run, one concurrent run/server.
Timeout range is 1–86,400 seconds; concurrency range is 1–64. Token accounting is
checked at model usage events and includes input, output and cached input; an
in-flight model response can overshoot the budget. It is not a hard billing cap.
The sample workshop configuration permits 900 seconds and 500,000 tokens.
Parallel runs have separate conversations but may share a configured workspace,
so keep concurrency at one when concurrent file edits would conflict.

## Authentication and remote binding

Non-loopback addresses, including `0.0.0.0` and `::`, require a bearer token.
Configure exactly one of:

```yaml
server:
  host: 0.0.0.0
  port: 8081
  bearer_token_env: YOLOMANCER_API_TOKEN
```

or `bearer_token: "your-random-secret-at-least-32-characters"`. Tokens must be
32–4096 characters without whitespace. A configured environment variable must
exist and be nonempty, even on loopback. Generate a secret with `openssl rand -hex
32`; don't commit it. Environment tokens are read at startup, not per request,
and the configured token environment variable is omitted from worker environments.

Every endpoint, including health checks, requires `Authorization: Bearer TOKEN`
when authentication is configured. Loopback without a token is intended only for
trusted local processes. The server rejects browser Origin headers, cross-site
requests and unauthenticated non-local Host headers; it does not enable CORS.
For a browser integration, use your trusted backend as the API client.

The listener speaks HTTP. Put TLS termination and any per-user authorization in
front of a remotely exposed server. All clients sharing the configured token
share the same authority, including cancellation of each other's active runs.
This is not a multi-tenant identity system. Binding locally also does not prevent
an allowed yolo agent from reading same-user files or using its OS credentials.

## HTTP interface

`GET /healthz` returns `{"ok":true}` when the HTTP service is listening. It does
not perform a model/AWS health check.

`GET /v1/agents` lists agent names, tool names, permission mode and timeout.

`POST /v1/runs` accepts only `agent` and `prompt`, and streams server-sent events:

```sh
curl -N http://127.0.0.1:8081/v1/runs \
  -H 'Content-Type: application/json' \
  -d '{"agent":"workshop","prompt":"List the files in documentation and summarise the API."}'
```

For authenticated servers add `-H "Authorization: Bearer $YOLOMANCER_API_TOKEN"`.
The body limit is 64 KiB; prompt limit is 32 KiB. Each request starts a fresh
conversation. There is currently no HTTP session-resume, event replay or automatic
retry/idempotency key. Retrying a disconnected request creates a new run and may
repeat side effects. Caller-provided conversation history is not accepted.

Each event has a run ID, monotonically increasing sequence and typed data:

```text
id: 1
event: run.started
data: {"type":"run.started","run_id":"...","sequence":1,"data":{"agent":"workshop"}}

id: 2
event: assistant.delta
data: {"type":"assistant.delta","run_id":"...","sequence":2,"data":{"text":"Let me inspect that."}}
```

Events:

| Type | Data |
| --- | --- |
| `run.started` | Agent name; run ID is in the envelope |
| `reasoning.delta` | Provider-exposed reasoning text, when available |
| `assistant.delta` | Incremental assistant text |
| `assistant.message` | Complete assistant message when not streamed |
| `assistant.done` | End of an assistant message, not necessarily the run |
| `tool.started` | `call_id`, `name`, `arguments` |
| `tool.completed` | `call_id`, `name`, `result` (including tool errors) |
| `permission_denied` | Tool-call ID, tool name, denial explanation |
| `usage.updated` | Provider token counters for a model request |
| `info` | Progress such as context compaction |
| `run.completed` | Final `text` |
| `run.blocked` | Terminal `permission_denied` code and explanation |
| `run.failed` | Terminal failure code/message |
| `run.cancelled` | Terminal cancellation |

SSE comment heartbeats are sent while idle. Text/tool results can contain sensitive
workspace data: treat the stream like the CLI transcript. Hidden provider reasoning
is not exposed or reconstructed. Wait for a terminal `run.*` event; a closed stream
without one is not proof of success. Completion text may repeat streamed text.

Permission denial never prompts, auto-approves or escalates. The tool is not
executed; a failed tool result is recorded/emitted, followed by the permission
event and `run.blocked`. Other tool errors may be recoverable by the agent.
There is no fixed 24-iteration limit; deadlines and token budgets bound a run.

`DELETE /v1/runs/RUN_ID` cancels an active run and returns 202; unknown/finished
runs return 404. Closing the POST stream also cancels the run. Server shutdown
cancels all active runs. Worker subprocesses are terminated and reaped; ordinary
descendants in their process group are cleaned up. Interactive PTYs (`tty:true`)
are denied. Deliberately daemonised/escaped processes created by trusted yolo
code are not a supported workload and require container-level cleanup.

Before streaming, errors use JSON and HTTP status: 400 invalid input, 401 missing
or incorrect token, 403 forbidden browser/Host, 404 unknown endpoint/agent/run,
429 concurrency exhausted. After streaming starts, failures use SSE terminal
events rather than changing HTTP status. Token exhaustion uses
`token_budget_exceeded`; run timeout uses `deadline_exceeded`.

## Credentials and architecture

The server starts a new worker process per request, with the configured workspace
as cwd and an explicit tool registry. The worker loads credentials from the
service user's `~/.yolomancer/config.toml`, or the `serve --profile` selection.
Registered accounts/profiles assume the existing Bedrock access role for model
calls. Tools retain the participant/profile identity; they do not inherit the
model's assumed-role credentials. The case-law extension still performs its own
purpose-specific role assumption.

Workers do not persist API conversations in the interactive CLI's session/goal
files. Local YAML policy overrides—not inherited CLI permission environment
variables or remembered approvals—control API execution. The HTTP caller cannot
select a different role, tools directory or permission mode.

The service is suitable for a container supervisor. Lambda adaptation is not part
of this command: it would need the Lambda request/streaming lifecycle adapter.
