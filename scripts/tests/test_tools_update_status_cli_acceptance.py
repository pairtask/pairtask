"""Read-only status reports retained stores through the actual host commands."""

import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
FAKE_INCUS = r'''
import json, os, sys
args = sys.argv[1:]
with open(os.environ['STATUS_CALLS'], 'a') as log:
    log.write(json.dumps(args) + '\n')
if 'list' in args:
    print(json.dumps([{'name':role,'status':'Running'} for role in ('workspace','secured')]))
    sys.exit(0)
if 'exec' not in args:
    sys.exit('unexpected mutating Incus command')
role = 'workspace' if any(arg.endswith(':workspace') for arg in args) else 'secured'
command = args[args.index('--') + 1:]
if command[:2] == ['python3','-c']:
    print(json.dumps({'marker':{'layout':2}}))
elif command[0].endswith('tools-activate') and command[1:] == ['status']:
    if os.environ.get('STATUS_FAILED_ROLE') == role:
        sys.exit('container unavailable')
    if role == 'secured' and 'STATUS_BAD_REPLY' in os.environ:
        print(os.environ['STATUS_BAD_REPLY']); sys.exit(0)
    count, size = (3, 3 * 1024 * 1024) if role == 'workspace' else (2, 2 * 1024 * 1024)
    print(json.dumps({'retained':{'generations':count,'stored_bytes':size}, 'complete':[]}))
elif command[0] == 'cat':
    print('{}')
else:
    sys.exit('unexpected mutating container command')
'''


class UpdateStatusCLIAcceptance(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.state = self.root / 'state'
        self.state.mkdir()
        executable = self.root / 'incus'
        executable.write_text('#!' + sys.executable + '\n' + FAKE_INCUS)
        executable.chmod(0o755)
        self.log = self.root / 'calls.jsonl'
        self.env = {**os.environ, 'PATH': str(self.root) + os.pathsep + os.environ['PATH'],
                    'PAIRTASK_OPERATOR_HOME': str(self.root / 'operator'), 'STATUS_CALLS': str(self.log),
                    'PAIRTASK_ALLOW_ROOT': '1'}

    def invoke(self, script, *args):
        result = subprocess.run([sys.executable, str(ROOT / 'scripts' / script), *args], env=self.env,
                                capture_output=True, text=True, timeout=20)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        return result.stdout

    def assert_usage(self, output):
        self.assertRegex(output, r'(?im)^.*workspace.*3 retained generations.*3(?:\.0+)? MiB stored')
        self.assertRegex(output, r'(?im)^.*secured.*2 retained generations.*2(?:\.0+)? MiB stored')
        self.assertFalse((self.state / 'tools-update.json').exists(), 'status created a transaction')

    def test_update_status_reports_both_stores_without_a_transaction(self):
        output = self.invoke('sandbox-tools-update.py', '--status', '--remote', 'local', '--state-dir', str(self.state))
        self.assert_usage(output)

    def test_pairtask_status_includes_retention_cost(self):
        output = self.invoke('pairtask.py', 'status', '--remote', 'local', '--project', 'status-proof-no-state')
        self.assert_usage(output)
        self.assertFalse((self.root / 'operator').exists(), 'status created a deployment registry')

    def test_unavailable_store_is_explicit_and_other_store_still_reported(self):
        self.env['STATUS_FAILED_ROLE'] = 'secured'
        output = self.invoke('pairtask.py', 'status', '--remote', 'local', '--project', 'status-proof-no-state')
        self.assertRegex(output, r'(?im)^.*workspace.*3 retained generations')
        self.assertRegex(output, r'(?im)^.*secured.*(?:unavailable|unknown)')
        self.assertNotRegex(output, r'(?im)^.*secured.*0 retained generations')

    def test_malformed_store_reply_is_unavailable_without_losing_other_role(self):
        replies = ['not JSON', '[]', '{"retained":{"generations":-1,"stored_bytes":-1}}']
        for reply in replies:
            with self.subTest(reply=reply):
                self.env['STATUS_BAD_REPLY'] = reply
                output = self.invoke('pairtask.py', 'status', '--remote', 'local', '--project', 'status-proof-no-state')
                self.assertRegex(output, r'(?im)^.*workspace.*3 retained generations')
                self.assertRegex(output, r'(?im)^.*secured.*(?:unavailable|unknown)')


if __name__ == '__main__':
    unittest.main()
