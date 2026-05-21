# yolomancer

`yolomancer` is a lightweight agentic coding CLI for a vibe-coding workshop.

It keeps the useful shape of a coding-agent terminal UI, but the tool surface is local and intentionally small so participants can re-imagine it during the training.

## Features

- Uses an OpenAI-compatible `POST /v1/responses` endpoint.
- Streams assistant output live from `/v1/responses` SSE events.
- Interactive bottom-bar terminal UI with transcript, input history, markdown rendering, and slash commands.
- Local workspace confinement for file tools and shell working directories.
- Interactive shell, network, and filesystem approval prompts.
- Local coding tools exposed to the model:
  - `shell`
  - `read_file`
  - `write_file`
  - `replace_in_file`
  - `list_files`
- Workshop sample tools exposed by this CLI:
  - `repo_snapshot`
  - `workshop_exercise`
- Workspace Python tools from `tools/*.py`, executed by embedded RustPython.

## Build

```bash
cargo build --release
```

## Configure

Store AWS Bedrock credentials and verify Opus access:

```bash
yolomancer login --profile your-aws-profile
yolomancer login
yolomancer login \
  --aws-access-key-id YOUR_AWS_ACCESS_KEY_ID \
  --aws-secret-access-key YOUR_AWS_SECRET_ACCESS_KEY \
  --aws-region us-east-1
```

Config is stored in `~/.yolomancer/config.toml`.

By default the workshop route uses AWS Bedrock Opus through the Converse API. The login command sends a tiny verification request and, when AWS reports that Anthropic use-case details are missing, submits the standard Bedrock use-case form and retries.

You can override the Bedrock model or inference profile:

```bash
yolomancer login --profile your-aws-profile --bedrock-model global.anthropic.claude-opus-4-7
```

## Usage

Interactive:

```bash
yolomancer
```

One-shot:

```bash
yolomancer run "inspect this repo and propose a small CLI improvement"
```

Preserve normal terminal scrollback:

```bash
yolomancer --no-alt-screen
```

Debug transport and tool execution:

```bash
yolomancer --debug
```

## Controls

- `ArrowUp` / `ArrowDown`: navigate submitted input history.
- `PageUp` / `PageDown`: scroll the transcript.
- `Ctrl-U` / `Ctrl-D`: scroll half a page.
- `Shift-Enter`: insert a newline when the terminal reports shifted Enter.
- `Esc`: clear the composer when idle; interrupt while a response is running.
- `/`: open the slash-command picker.
- `Ctrl-C` or `:quit`: exit.

## Workshop Hooks

The sample tools are deliberately simple:

- `repo_snapshot` summarizes the local workspace with sampled paths and extension counts.
- `workshop_exercise` returns a short exercise brief for the training session.
- `tools/reverse_text.py` is a user-extensible Python tool loaded from the workspace.

Workspace Python tools use a `yolomancer_tool()` metadata function and a `run(args)` function:

```python
def yolomancer_tool():
    return {
        "name": "reverse_text",
        "description": "Reverse text.",
        "parameters": {
            "type": "object",
            "properties": {"text": {"type": "string"}},
            "required": ["text"],
            "additionalProperties": False,
        },
    }

def run(args):
    text = args.get("text", "")
    return {"ok": True, "text": text[::-1]}
```

During tool discovery, yolomancer extracts and executes only the `yolomancer_tool()` block. Other imports and top-level code are not interpreted until the tool is actually called. The `parameters` value is JSON Schema for the tool arguments. yolomancer adds its required `reason` field automatically before exposing the tool to the model. `run(args)` receives a Python dict and should return a JSON-serializable dict or a JSON string. Python is embedded with RustPython, so users do not need a separate Python install.

Python tools can also use AWS after a role is configured at runtime:

```text
/sudo arn:aws:iam::<account-id>:role/<role-name>
```

```python
import yolomancer_aws as aws

def run(args):
    return {"identity": aws.sts.get_caller_identity()}
```

yolomancer assumes the configured role inside Rust and never returns temporary credentials to Python or the transcript.
SDK-backed helpers are exposed as Python namespaces. Each Rust-owned helper has a known permission scope (`read`, `write`, `destructive`, or `unknown`) attached before the AWS call is made.

```python
import yolomancer_aws as aws

def run(args):
    return {
        "identity": aws.sts.get_caller_identity(),
        "buckets": aws.s3.list_buckets(),
        "tables": aws.dynamodb.list_tables(),
        "vpcs": aws.ec2.describe_vpcs(),
    }
```

Available namespaces include `sts`, `s3`, `iam`, `ec2`, `dynamodb`, `cloudformation`, `route53`, and `account`. `aws.request(service, method, url, body="", headers=None, region=None)` remains available as a generic signed HTTPS escape hatch and is classified as `unknown`.

The bundled `aws_tool` exposes these helpers to the model through one action-based tool:

```json
{
  "action": "s3.list_buckets",
  "arguments": {}
}
```

It also includes built-in help:

```json
{
  "action": "help",
  "arguments": {"service": "cloudformation"}
}
```

Good participant tasks:

- Rename the product concept again and make the UI copy coherent.
- Replace `workshop_exercise` with a domain-specific tool.
- Add a tool that reads project metadata, test status, TODOs, or architectural boundaries.
- Change permissions or approval UX for a specific teaching scenario.

## Security Behavior

- File tools are restricted to the current workspace unless approved.
- Shell working directories are restricted to the current workspace unless approved.
- Dangerous shell commands require explicit interactive approval.
- Networked shell commands can be allowed, approval-gated, or denied separately.
- Trusted projects can opt into `danger-full-access`; untrusted projects default to `workspace-write`.
- Protected subpaths such as `.git` and `.yolomancer` remain read-only inside writable roots while `workspace-write` mode is active.
