# vibecode-cli

`vibecode-cli` is a lightweight agentic coding CLI for a vibe-coding workshop.

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

## Build

```bash
cargo build --release
```

## Configure

Store AWS Bedrock credentials and verify Opus access:

```bash
vibecode-cli login --profile your-aws-profile
vibecode-cli login
vibecode-cli login \
  --aws-access-key-id YOUR_AWS_ACCESS_KEY_ID \
  --aws-secret-access-key YOUR_AWS_SECRET_ACCESS_KEY \
  --aws-region us-east-1
```

Config is stored in `~/.vibecode/config.toml`.

By default the workshop route uses AWS Bedrock Opus through the Converse API. The login command sends a tiny verification request and, when AWS reports that Anthropic use-case details are missing, submits the standard Bedrock use-case form and retries.

You can override the Bedrock model or inference profile:

```bash
vibecode-cli login --profile your-aws-profile --bedrock-model global.anthropic.claude-opus-4-7
```

## Usage

Interactive:

```bash
vibecode-cli
```

One-shot:

```bash
vibecode-cli run "inspect this repo and propose a small CLI improvement"
```

Preserve normal terminal scrollback:

```bash
vibecode-cli --no-alt-screen
```

Debug transport and tool execution:

```bash
vibecode-cli --debug
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
- Protected subpaths such as `.git` and `.vibecode` remain read-only inside writable roots while `workspace-write` mode is active.
