#!/usr/bin/env python3
"""Save only the checkout location, never credentials, for native plugin hooks."""
import json
from pathlib import Path
root = Path(__file__).resolve().parent.parent
if not (root / '.env').is_file():
    raise SystemExit('Run scripts/setup.sh first.')
config = Path.home() / '.config/codex-telegram-approve/hook.json'
config.parent.mkdir(parents=True, exist_ok=True)
config.write_text(json.dumps({'root': str(root), 'bridge_url': 'http://127.0.0.1:8787'}, indent=2) + '\n')
config.chmod(0o600)
print('Native Telegram hook configured. Refresh the plugin, restart cx, then review it in /hooks.')

# Codex 0.162.1 may omit plugin-bundled hooks from runtime discovery.
# Register the same handler in the documented user hook layer instead.
import os
import shlex
hooks_path = Path(os.environ.get('CODEX_HOME', str(Path.home() / '.codex'))) / 'hooks.json'
hooks = json.loads(hooks_path.read_text()) if hooks_path.exists() else {'hooks': {}}
groups = hooks.setdefault('hooks', {}).setdefault('PermissionRequest', [])
command = 'python3 ' + shlex.quote(str(root / 'plugins/codex-telegram-approve/hooks/permission_request.py'))
if not any(handler.get('command') == command for group in groups for handler in group.get('hooks', [])):
    groups.append({'hooks': [{'type': 'command', 'command': command,
        'timeout': 1860, 'statusMessage': 'Checking approval mode (Computer / Telegram)'}]})
    hooks_path.parent.mkdir(parents=True, exist_ok=True)
    hooks_path.write_text(json.dumps(hooks, indent=2) + '\n')
print('Registered the Telegram handler in user hooks. Existing hooks and trust are preserved.')
