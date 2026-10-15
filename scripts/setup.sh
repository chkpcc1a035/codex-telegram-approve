#!/usr/bin/env bash
set -euo pipefail
cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.."
umask 077
if [[ -e .env ]]; then
  echo '.env already exists. Edit it locally; setup will not overwrite your secrets.' >&2
  exit 1
fi
command -v openssl >/dev/null || { echo 'Install openssl first.' >&2; exit 1; }
echo 'Create a Telegram bot using @BotFather, then send /start to that bot.'
read -rsp 'Telegram bot token (hidden; kept in local .env): ' bot_token
echo
[[ "$bot_token" =~ ^[0-9]+:[A-Za-z0-9_-]+$ ]] || { echo 'Invalid token format.' >&2; exit 1; }
echo 'To discover IDs, use the pair command in the README before running this wizard.'
read -rp 'Your numeric Telegram user ID (the person allowed to approve): ' user_id
read -rp 'Target chat ID (may be negative for a group): ' chat_id
[[ "$user_id" =~ ^[1-9][0-9]*$ && "$chat_id" =~ ^-?[1-9][0-9]*$ ]] || { echo 'Invalid numeric IDs.' >&2; exit 1; }
bridge_token=$(openssl rand -hex 32)
cat > .env <<EOF
TELEGRAM_BOT_TOKEN=$bot_token
TELEGRAM_USER_ID=$user_id
TELEGRAM_CHAT_ID=$chat_id
BRIDGE_TOKEN=$bridge_token
APPROVAL_TIMEOUT=5m
EOF
chmod 600 .env
unset bot_token bridge_token
echo 'Saved .env with mode 600. Next: docker compose up -d --build'
echo 'Run a session with: ./scripts/codex-telegram --cwd /path/to/project'
