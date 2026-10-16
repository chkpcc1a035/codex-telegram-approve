import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('hook', Path(__file__).resolve().parents[1] / 'plugins/codex-telegram-approve/hooks/permission_request.py')
hook = importlib.util.module_from_spec(spec)
spec.loader.exec_module(hook)

class HookTests(unittest.TestCase):
    def test_mode_command_and_hook_share_setting(self):
        root = Path(__file__).resolve().parents[1]
        with tempfile.TemporaryDirectory() as directory:
            env = dict(os.environ, HOME=directory)
            config = Path(directory) / '.config/codex-telegram-approve'
            config.mkdir(parents=True)
            (config / 'hook.json').write_text('{}')
            def command(mode):
                return subprocess.run([sys.executable, str(root / 'scripts/approval-mode'), mode], env=env, text=True, capture_output=True, check=True).stdout
            self.assertIn('Computer', command('status'))
            self.assertIn('Telegram', command('toggle'))
            self.assertEqual(json.loads((config / 'mode.json').read_text())['mode'], 'telegram')
            self.assertIn('Computer', command('toggle'))
            result = subprocess.run([sys.executable, str(root / 'plugins/codex-telegram-approve/hooks/permission_request.py')], env=env, input='{"hook_event_name":"PermissionRequest"}', text=True, capture_output=True, check=True)
            self.assertEqual(json.loads(result.stdout), {})
            (config / 'mode.json').write_text('broken')
            self.assertIn('Computer', command('computer'))

    def test_computer_mode_defers_without_contacting_bridge(self):
        out = io.StringIO()
        event = {'hook_event_name': 'PermissionRequest'}
        with patch.object(hook, 'read_mode', return_value='computer'), patch.object(hook, 'decision') as decision, patch.object(hook.sys, 'stdin', io.StringIO(json.dumps(event))), contextlib.redirect_stdout(out):
            hook.main()
        self.assertEqual(json.loads(out.getvalue()), {})
        decision.assert_not_called()

    def test_concurrent_mode_defers_to_patched_native_reviewer(self):
        out = io.StringIO()
        with patch.object(hook, 'read_mode', return_value='concurrent'), patch.object(hook, 'decision') as decision, patch.object(hook.sys, 'stdin', io.StringIO('{"hook_event_name":"PermissionRequest"}')), contextlib.redirect_stdout(out):
            hook.main()
        self.assertEqual(json.loads(out.getvalue()), {})
        decision.assert_not_called()

    def test_telegram_mode_keeps_explicit_allow_and_deny(self):
        for allowed in (True, False):
            out = io.StringIO()
            with patch.object(hook, 'read_mode', return_value='telegram') as mode, patch.object(hook, 'decision', return_value=allowed), patch.object(hook.sys, 'stdin', io.StringIO('{"hook_event_name":"PermissionRequest"}')), contextlib.redirect_stdout(out):
                hook.main()
            self.assertEqual(json.loads(out.getvalue())['hookSpecificOutput']['decision']['behavior'], 'allow' if allowed else 'deny')
            mode.assert_called_once()

    def test_mode_defaults_and_invalid_setting(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'mode.json'
            with patch.object(hook, 'MODE_CONFIG', path):
                self.assertEqual(hook.read_mode(), 'computer')
                for value in ('computer', 'telegram', 'concurrent'):
                    path.write_text(json.dumps({'mode': value}))
                    self.assertEqual(hook.read_mode(), value)
                for value in ('{}', '{', '{"mode":"allow"}', 'null'):
                    path.write_text(value)
                    out = io.StringIO()
                    with patch.object(hook.sys, 'stdin', io.StringIO('{"hook_event_name":"PermissionRequest"}')), contextlib.redirect_stdout(out):
                        hook.main()
                    self.assertEqual(json.loads(out.getvalue())['hookSpecificOutput']['decision']['behavior'], 'deny')

    def test_decisions_and_crlf(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / '.env').write_bytes(b'BRIDGE_TOKEN=' + b'x'*32 + b'\r\nTELEGRAM_BOT_TOKEN=do-not-send\r\n')
            config = root / 'hook.json'
            config.write_text(json.dumps({'root': str(root)}))
            event = {'hook_event_name': 'PermissionRequest', 'tool_name': 'Bash', 'tool_input': {'command': 'echo test'}}
            for choice, expected in [('accept', True), ('decline', False), ('unexpected', None)]:
                class Opener:
                    def open(self, request, timeout):
                        self.request = request
                        assert request.get_header('Authorization') == 'Bearer ' + 'x'*32
                        assert json.loads(request.data)['params'] == event
                        assert b'do-not-send' not in request.data
                        return contextlib.closing(io.BytesIO(json.dumps({'decision': choice}).encode()))
                with patch.object(hook, 'CONFIG', config), patch.object(hook.urllib.request, 'build_opener', return_value=Opener()):
                    if expected is None:
                        with self.assertRaises(ValueError): hook.decision(event)
                    else:
                        self.assertEqual(hook.decision(event), expected)

    def test_failure_returns_explicit_deny_without_secret(self):
        out = io.StringIO()
        with patch.object(hook.sys, 'stdin', io.StringIO('{}')), patch.object(hook, 'decision', side_effect=Exception('SECRET')), contextlib.redirect_stdout(out):
            hook.main()
        result = json.loads(out.getvalue())['hookSpecificOutput']
        self.assertEqual(result['decision']['behavior'], 'deny')
        self.assertNotIn('SECRET', out.getvalue())

    def test_redirect_rejected(self):
        with self.assertRaises(ValueError): hook.NoRedirect().redirect_request(None)

if __name__ == '__main__': unittest.main()
