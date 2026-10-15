import contextlib
import importlib.util
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('hook', Path(__file__).resolve().parents[1] / 'plugins/codex-telegram-approve/hooks/permission_request.py')
hook = importlib.util.module_from_spec(spec)
spec.loader.exec_module(hook)

class HookTests(unittest.TestCase):
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
