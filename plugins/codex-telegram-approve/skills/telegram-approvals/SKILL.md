---
name: telegram-approvals
description: Set up, check, or troubleshoot the codex-telegram-approve Go backend and local launcher when the user wants to approve Codex actions from Telegram.
---

# Telegram approvals

This plugin includes a native `PermissionRequest` handler and setup guidance for the
`codex-telegram-approve` repository. Prefer the native hook to preserve the full
Codex UI and HUD wrappers such as `cx`. After backend setup, run
`python3 scripts/configure-hook.py` from the checkout, refresh the plugin and
restart Codex. Setup registers the handler in user hooks.json because 0.162.1
did not discover bundled hooks in our runtime check. Bundled declarations are
disabled to avoid duplicates; disabling the plugin does not remove the user hook.
The user must review and trust the hook through `/hooks`; never
bypass or write hook trust on their behalf. The native hook requires Python 3
and a Codex version supporting PermissionRequest (locally checked: 0.162.1).
Existing approval and sandbox policy remain in effect. Hook failures explicitly
deny; untrusted hooks are skipped by Codex. The basic `scripts/codex-telegram`
App Server launcher remains a fallback, with its own line-based interface.

## Find the running installation

Use the repository path already established in the conversation or the user's
`CODEX_TELEGRAM_APPROVE_ROOT` environment variable. If the current directory
contains `server.go`, `compose.yaml`, and `scripts/codex-telegram`, it is also a
candidate. Otherwise ask for the checkout path. The installed plugin cache is
not the application checkout: it intentionally contains no `.env`, host binary,
or backend source. Do not create a second backend or copy secrets into the cache.

Read that checkout's README for the maintained installation procedure. Check
the binary and `.env` exist without displaying secret values. Check liveness
using `curl --fail http://127.0.0.1:8787/healthz`, or the user's configured bridge
URL. A successful health check does not verify Telegram delivery or Codex login.

## Set up or repair

- Build a missing host client with `make build` in the checkout; Go 1.24+ is
  required. The Docker backend alone cannot run local workspace sessions.
- For a missing configuration, guide the user to create a bot through
  `@BotFather`, discover numeric IDs with `bin/codex-telegram-approve pair`, and
  run `bash scripts/setup.sh` in their own terminal. The token belongs in local
  hidden input, never the conversation or plugin manifest. Do not print or
  source `.env`.
- Telegram user IDs are numeric account identifiers, not `@usernames`. Private
  bot chats have the same user and chat ID; group chat IDs are typically negative.
- Start the backend with `docker compose up -d --build` when needed. After a
  configuration edit, recreate it to reload `.env`. Preserve a healthy existing
  backend. Check `docker compose logs --tail 50 approvals` for startup errors.
- Run only one Telegram poller per bot. Stop `pair` before starting the backend.
  `/start` enables bot messaging but this version does not reply to that command.

## Launch a connected session

Provide a command in the user's own terminal, substituting the established
checkout path and their chosen project directory:

```bash
/path/to/codex-telegram-approve/scripts/codex-telegram \
  --cwd /path/to/project --approval untrusted
```

Use `on-request` when the user prefers fewer prompts. The client uses
`workspace-write` and the user's existing Codex login. Do not change global Codex
approval policy or disable sandboxing to make this plugin work. Do not launch a
nested interactive session inside an agent tool call; give the terminal command.

Only gated actions send Telegram messages. The supported requests are command
execution, file changes, and permission grants. Permission approvals apply for
the current turn; other approvals are for the request. Questions, MCP elicitation,
and connector-specific forms are unsupported. Do not describe this plugin as
intercepting another running client's pending approval.
