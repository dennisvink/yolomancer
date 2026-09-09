# Yolomancer lab

Endpoint: https://lab.yolomancer.com

Infrastructure: `../../terraform/lab/`, with separate local Terraform state.
This directory is intentionally gitignored; preserve its state securely.

- Profile `prosus-user-000`: account 577995596848, eu-west-1 lab infrastructure.
- Profile `default`: account 183305290766, DNS and credential dispenser.

## Identity and recovery

The browser submits a code over HTTPS to the gateway. The gateway makes a
SigV4-authenticated request through API Gateway to Lambda. Only the gateway IAM
role is granted access to this API. Lambda validates the code and generates or
recovers an Ed25519 identity. Its seed is encrypted with KMS and stored under
`ssh#<code hash>` in the dispenser table. Concurrent requests converge on the
same identity using conditional DynamoDB writes.

The gateway returns the keypair to the browser, which saves it in IndexedDB.
The workspace receives the same key as `~/.ssh/id_ed25519` (0600) and
`~/.ssh/id_ed25519.pub` (0644), in a directory with mode 0700. SSH authorized_keys
uses that public key. Host-key verification pins the workspace's SSH key.

The access code is a recovery secret. Anyone possessing a valid code can
recover its private key and reconnect from another browser. Previously claimed
codes are accepted. Recovery overrides the dispenser registration identity when
creating a new workspace, without deleting credentials from previous clients.
Repeated requests for a live workspace do not create additional tasks.

Legacy browser-only identities cannot be recovered. Their workspace must be
stopped before migration to the backend-generated identity; this loses ephemeral
files. A stopped workspace can be replaced with a fresh one using the same
unexpired code and the same recovered SSH identity.

## Workspace

Source is cloned directly into `/workspace`, after SSH identity provisioning,
and built there. The resulting `/workspace/yolomancer` has mode 0755 and is
symlinked from both `/usr/bin/yolomancer` and `/usr/local/bin/yolomancer`.
The bootstrap runs `yolomancer register <code>`, then prepares AWS CLI
credentials with mode 0600. Interactive SSH starts a shell in `/workspace`;
Yolomancer is not started automatically.

GitHub CLI is installed. Without participant GitHub authentication, provisioning
uses anonymous HTTPS git clone for the public repository. With authentication,
it uses gh repo clone. No operator GitHub token is shared. SSH uses BatchMode
and StrictHostKeyChecking=accept-new, accepting unknown keys but rejecting changed
ones. The generated public key is not automatically registered with GitHub.

Installed tools include AWS CLI, Terraform, GitHub CLI, Git, Go, Node/npm,
Python/pip/venv, C/C++ build tools, jq, ripgrep, tmux, and terminal editors.
Participant has passwordless sudo inside its own container. There is no Docker
daemon and no ECS task role. AWS tooling uses the participant credentials.

Outbound traffic is unrestricted on all ports. Inbound SSH is gateway-only.

## Lifetime

Each new workspace expires 48 hours after provisioning, or at code
expiry if sooner. Files are ephemeral. Closing a tab does not stop the task.
The container shuts down at its deadline, and an external scheduled Lambda
also stops expired tasks so sudo cannot bypass the deadline.

Existing workspaces retain the deadline set when they were provisioned. They
are not restarted automatically when the lifetime default changes. Reinstalling
creates a new workspace with the current default and discards the old files.

## Delete and reinstall

The header controls show confirmation dialogs warning of irreversible loss of
all workspace files and changes. Cancel does not send an action request.
Reinstall and Delete Lab are visibly disabled during provisioning and shutdown,
and while an action is in flight. They are available after connection or when
setup has failed/the task has stopped. `tests/controls.cjs` checks these UI states
without creating or mutating a lab.

Delete Lab stops the current task, retains the browser's session and saved key,
and leaves a deletion marker to catch any in-flight provisioning.
It does not revoke the AWS credentials, invalidate the access code, or remove
the recoverable backend identity.

Reinstall marks the old generation as shutting down before requesting StopTask.
Once AWS acknowledges that request, it provisions a fresh task with the saved
code, without waiting for the old task to reach STOPPED. Progress and readiness
writes are conditionally fenced against the old payload and shutdown marker,
including requests already in flight. Replacement atomically changes the
dispenser signing-key binding so the old registration key cannot overwrite it.
The browser does not need to enter the code again. Repeated requests for the
same operation return the same replacement. Reloading resumes a pending
reinstall. Both actions require the authenticated cookie, same-origin POST,
explicit confirmation and matching generation; a stale tab cannot delete a
newer generation. A scheduled cleanup also stops deleted or superseded tasks.

Provisioning with a saved identity sends its public key and an Ed25519 signature
binding the access code, timestamp, and nonce. The recovered backend identity
must match; the browser's private key is never uploaded. Re-entering the same
valid code restores that key into the replacement workspace. Only Reset clears
the browser identity. A different code selects its own saved/backend identity.
Missing/expired browser sessions offer code entry without discarding
the saved key; an ECS task that no longer exists is treated as stopped.

## Browser terminal tabs and clipboard

The root page hosts persistent terminal tabs. Each iframe has its own Go WASM
runtime, xterm, and SSH connection. Switching hides/shows the frame without
disconnecting; closing removes the connection but does not delete the lab.
The tab manifest records open entries and closed entries with a reason and
timestamp. Refresh restores only open entries. A remote SSH shell exit (including
nonzero exit status) closes its tab; transport failures leave the tab open for
reconnection. Page unload and explicit reinstall suppress close notifications.
Workspace STOPPED/missing status also closes a restored terminal tab. A closed
tab's saved SSH identity remains available when its code is entered again.
If no tabs remain, a blank code-entry tab is opened. Close controls are on the
left, and the new-tab button follows the last tab with a 6px gap.
New tabs always start at code entry. IndexedDB stores identities by the SHA-256
code/assignment ID, never the plaintext access code; entering the same code
selects its saved key. LocalStorage stores tab IDs, assignment IDs, and masked
labels. Reload restores tabs and reconnects them. The prior single-key/session
format migrates into the initial tab.

Each tab selects an independent HttpOnly `lab_t-<uuid>` cookie. HTTP uses the
`X-Lab-Tab` header; WebSocket uses a non-secret `tab` query selector. The server
still authenticates the selected cookie and pins relay destination/host key.
The selector alone grants no access. Reset clears the selected tab cookie and
its code's saved identity, not identities for other codes. The standalone
`/terminal.html` endpoint remains available for compatibility and tests.

Right-click a tab and choose Duplicate to insert a new terminal immediately
beside it, using the same saved identity and running workspace. The authenticated
gateway copies the session cookie into the new tab's cookie; it does not provision
a task or rotate credentials. Duplicating a blank tab opens another blank tab.

Yolomancer leaves mouse capture disabled so terminal text is directly selectable.
Copy selection with Cmd+C/Ctrl+Shift+C; there are no Copy/Paste toolbar buttons.
Paste reads clipboard text into xterm without appending
Enter; multi-line clipboard text prompts because embedded newlines may execute
shell commands. Browser-native paste and Cmd+V/Ctrl+Shift+V are supported;
clipboard permissions may be requested by the browser. Ctrl+C without a
selection retains its normal terminal interrupt behavior.

`tests/tabs.cjs` tests tab identity isolation, reload, reset, and clipboard with
mocked API/SSH. `tests/tabs-live.cjs` uses two synthetic codes and disposable
Fargate labs to verify actual simultaneous SSH connections and same-code reuse.

Gateway and ALB stay running and billable between sessions. Gateway deployments
may disconnect SSH; reload to reconnect. CloudWatch logs are in
`/ecs/yolomancer-lab` and `/aws/lambda/yolomancer-lab-identity`.
Do not log access codes, private keys, bootstrap tokens, or credentials.

## Deploy

Build the two Dockerfiles under `ssheasy/lab/` with Docker context `ssheasy`
and `--platform linux/amd64`. Push to the `yolomancer-lab-gateway` and
`yolomancer-lab-workspace` ECR repositories in account 577995596848.
Set `image_tag` (workspace) and `gateway_image_tag` in the lab tfvars.

The workspace image preclones and builds the public Yolomancer repository as
`participant` in `/workspace`. Build with
`--build-arg YOLOMANCER_REVISION=<current main SHA>`; the required revision
invalidates Docker's source layer cache and is checked against the clone.
Refresh the argument whenever rebuilding the source snapshot. No lab identity,
access code, AWS credential, or operator GitHub token is supplied to this build.
`GOCACHE=/home/participant/.cache/go-build` and
`GOMODCACHE=/home/participant/go/pkg/mod` are retained in the actual image layers
and remain writable by the participant. They are not BuildKit-only cache mounts.
Provisioning fast-forwards `origin/main`, downloads only missing module versions,
and rebuilds using the same paths and flags. Failed source updates fail setup
rather than silently starting old code. This optimization applies only to new
or explicitly reinstalled labs; existing participant files are not updated.

Provisioning and Reinstall share a live progress view. Fargate startup/image
pull status comes from ECS; the workspace reports fixed stages to `/progress`
using its bootstrap token: SSH identity, clone, Go dependencies, build,
registration/AWS configuration, SSH startup, and ready. These are numbered
steps, not time estimates. Progress persists in the lab record so reloads
resume it. No command output or secrets are returned as progress. Reports
are conditional on the current generation and rejected after deletion or
replacement. Reporting failures do not abort otherwise successful setup.
Existing running workspaces are not replaced when deploying progress support.

Browsers with a saved identity hide the code form and show Connecting while
restoring the session. Reset requires confirmation and clears only IndexedDB
identity and the HttpOnly session cookie via same-origin `/api/reset`; it does
not stop a workspace or modify the dispenser. An expired/missing session still
offers Reset. `tests/reset.cjs` exercises this with a browser-only fake identity.

Package the identity Lambda before planning:

```sh
zip -j terraform/lab/identity.zip ssheasy/lab/identity/handler.mjs ssheasy/lab/gateway/auth.mjs ssheasy/lab/gateway/identity.mjs
terraform -chdir=terraform/lab plan
```

Review and apply the lab plan, not the parent dispenser stack. Do not destroy
the KMS key while stored identities are needed.

## Tests

Run `node --test ssheasy/lab/gateway/*.test.mjs` for key/signature tests.
The live test requires Playwright and both AWS profiles. It uses a synthetic,
marked record with non-working credentials, checks expired-code rejection,
used-code recovery, concurrent provisioning, source build, sudo, file modes,
and cross-browser SSH. It cleans up its own records and task. It does not
verify Bedrock access or consume real participant codes.

`ssheasy/lab/tests/actions.cjs` exercises 48-hour deadlines, modal cancellation,
confirmation and stale-generation rejection, real reinstall, retry idempotency,
and deletion using a synthetic code. It stops only its disposable tasks and
removes its records on exit.
