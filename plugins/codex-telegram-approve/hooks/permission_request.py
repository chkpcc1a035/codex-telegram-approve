#!/usr/bin/env python3
"""Forward native Codex approval hooks to the local Telegram bridge."""
import json
import os
from pathlib import Path
import socket
import sys
import urllib.request
import urllib.parse

CONFIG = Path.home() / '.config/codex-telegram-approve/hook.json'
MODE_CONFIG = CONFIG.with_name('mode.json')

def read_mode():
    """Missing settings use native approval; malformed settings fail closed."""
    try:
        settings = json.loads(MODE_CONFIG.read_text())
        mode = settings['mode']
        veto = settings.get('veto_seconds', 5)
        if type(veto) is not int or not 1 <= veto <= 60:
            raise ValueError('invalid veto window')
    except FileNotFoundError:
        return 'computer'
    if mode not in ('computer', 'telegram', 'concurrent'):
        raise ValueError('invalid approval mode')
    return mode

class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        raise ValueError('bridge redirects are forbidden')

def decision(event):
    if event.get('hook_event_name') != 'PermissionRequest':
        raise ValueError('unexpected hook event')
    config = json.loads(CONFIG.read_text())
    root = Path(config['root'])
    # Parse as data, never source shell code; splitlines handles Windows CRLF.
    token = None
    for line in (root / '.env').read_text().splitlines():
        key, sep, value = line.partition('=')
        if sep and key == 'BRIDGE_TOKEN':
            token = value
    if not token or len(token) < 32 or any(ord(c) < 33 or ord(c) > 126 for c in token):
        raise ValueError('invalid bridge credential')
    endpoint = config.get('bridge_url', 'http://127.0.0.1:8787')
    url = urllib.parse.urlsplit(endpoint)
    if (url.scheme not in ('http', 'https') or not url.hostname or url.username or
            url.password or url.query or url.fragment or
            (url.scheme == 'http' and url.hostname not in ('127.0.0.1', 'localhost', '::1'))):
        raise ValueError('invalid bridge URL')
    # Keep the entire hook event so the user can inspect the exact input.
    payload = {'method': 'item/commandExecution/requestApproval',
               'host': socket.gethostname() + ' native Codex hook', 'params': event}
    req = urllib.request.Request(endpoint.rstrip('/') + '/v1/approvals',
        data=json.dumps(payload).encode(), headers={
            'Authorization': 'Bearer ' + token, 'Content-Type': 'application/json'})
    opener = urllib.request.build_opener(NoRedirect())
    with opener.open(req, timeout=1850) as response:
        result = json.loads(response.read(4096))
    if result.get('decision') not in ('accept', 'decline'):
        raise ValueError('invalid bridge decision')
    return result['decision'] == 'accept'

def main():
    message = 'Denied in Telegram.'
    try:
        event = json.loads(sys.stdin.read(65537))
        if event.get('hook_event_name') != 'PermissionRequest':
            raise ValueError('unexpected hook event')
        # Snapshot once: changing modes never changes an in-flight decision.
        if read_mode() in ('computer', 'concurrent'):
            print('{}')
            return
        allowed = decision(event)
    except Exception:
        # URLs, request headers and credentials must never reach hook output.
        allowed = False
        message = 'Approval unavailable; denied. Check approval mode, bridge health and hook configuration.'
    result = {'behavior': 'allow' if allowed else 'deny'}
    if not allowed:
        result['message'] = message
    print(json.dumps({'hookSpecificOutput': {
        'hookEventName': 'PermissionRequest', 'decision': result}}))

if __name__ == '__main__':
    main()
