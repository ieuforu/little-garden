import hashlib
import io
import json
from pathlib import Path
import subprocess
import sys
import tarfile
import tempfile
import unittest
from unittest.mock import patch
import zipfile

import build_update
import update_entry


class UpdateTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.assets = self.root / 'assets'
        self.assets.mkdir()
        self.image = self.assets / update_entry.IMAGE
        self.image.write_bytes(b'CI image fixture')
        self.runtime = {name: f'unchanged {name}\n'.encode() for name in update_entry.RUNTIME_FILES}
        host = self.assets / 'host-files.tar.gz'
        with tarfile.open(host, 'w:gz') as bundle:
            for name, data in self.runtime.items():
                info = tarfile.TarInfo(name)
                info.size = len(data)
                bundle.addfile(info, io.BytesIO(data))
        (self.assets / 'SHA256SUMS').write_text(''.join(
            f'{hashlib.sha256(path.read_bytes()).hexdigest()}  {path.name}\n' for path in (host, self.image)))
        self.archive = self.root / 'update.zip'
        self.commit = 'a' * 40
        self.installed = self.root / 'installed'
        for name, data in self.runtime.items():
            target = self.installed / name
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(data)
        (self.installed / 'deploy/.env').write_bytes(b'secret fixture, preserve exactly\n')
        (self.installed / '.local/deploy').mkdir(parents=True)
        (self.installed / '.local/deploy/current').write_text('sha256:' + 'b' * 64)
        self.hashes = {name: hashlib.sha256(data).hexdigest() for name, data in self.runtime.items()}

    def verify(self):
        return subprocess.run([sys.executable, '-I', str(self.archive), '--verify-only'],
                              capture_output=True, text=True, timeout=10)

    def test_reproducible_verify_only_and_corrupted_image(self):
        first = build_update.build(self.assets, self.commit, self.archive)
        self.assertEqual(first, build_update.build(self.assets, self.commit, self.archive))
        self.assertEqual(self.verify().returncode, 0)
        with zipfile.ZipFile(self.archive) as bundle:
            entry = bundle.read('__main__.py')
        with zipfile.ZipFile(self.archive, 'w') as bundle:
            bundle.writestr('__main__.py', entry)
            bundle.writestr(update_entry.IMAGE, b'X' * self.image.stat().st_size)
        result = self.verify()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('镜像校验失败', result.stderr)

    def test_changed_runtime_or_missing_install_stops_before_any_docker_command(self):
        for name in ('scripts/deploy.sh', '.local/deploy/current'):
            target = self.installed / name
            original = target.read_bytes()
            target.write_bytes(b'unexpected')
            with patch.object(update_entry, 'RUNTIME_HASHES', self.hashes), patch.object(update_entry.subprocess, 'run') as run:
                with self.assertRaises(RuntimeError):
                    update_entry.apply_update(self.installed, self.image)
                run.assert_not_called()
            target.write_bytes(original)

    def test_wrong_image_revision_never_deploys(self):
        with patch.object(update_entry, 'RUNTIME_HASHES', self.hashes), patch.object(update_entry, 'COMMIT', self.commit), \
                patch.object(update_entry.subprocess, 'run') as run:
            run.return_value.stdout = json.dumps([{'Os': 'linux', 'Architecture': 'amd64', 'Config': {'Labels': {}}}])
            with self.assertRaises(RuntimeError):
                update_entry.apply_update(self.installed, self.image)
            self.assertFalse(any(call.args[0][0] == 'bash' for call in run.call_args_list))

    def test_update_uses_existing_backup_deployer_and_preserves_config(self):
        before = {str(path): path.read_bytes() for path in self.installed.rglob('*') if path.is_file()}
        with patch.object(update_entry, 'RUNTIME_HASHES', self.hashes), patch.object(update_entry, 'COMMIT', self.commit), \
                patch.object(update_entry.subprocess, 'run') as run:
            run.return_value.stdout = json.dumps([{'Os': 'linux', 'Architecture': 'amd64', 'Config': {
                'Labels': {'org.opencontainers.image.revision': self.commit}}}])
            update_entry.apply_update(self.installed, self.image)
            run.assert_called_with(['bash', str(self.installed / 'scripts/deploy.sh'), '--local',
                                    'v0.1.0-preview.' + self.commit[:12]], check=True)
            self.assertFalse(any('pull' in call.args[0] for call in run.call_args_list))
        self.assertEqual(before, {str(path): path.read_bytes() for path in self.installed.rglob('*') if path.is_file()})

    def test_failed_deploy_is_propagated(self):
        def run(command, **kwargs):
            if command[0] == 'bash':
                raise subprocess.CalledProcessError(1, command)
            return subprocess.CompletedProcess(command, 0, json.dumps([{'Os': 'linux', 'Architecture': 'amd64',
                'Config': {'Labels': {'org.opencontainers.image.revision': self.commit}}}]))
        with patch.object(update_entry, 'RUNTIME_HASHES', self.hashes), patch.object(update_entry, 'COMMIT', self.commit), \
                patch.object(update_entry.subprocess, 'run', side_effect=run):
            with self.assertRaises(subprocess.CalledProcessError):
                update_entry.apply_update(self.installed, self.image)


if __name__ == '__main__':
    unittest.main()
