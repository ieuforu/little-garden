import builtins
from contextlib import ExitStack, redirect_stdout
import io
import json
from pathlib import Path
import stat
import subprocess
import sys
import tarfile
import tempfile
import unittest
from unittest.mock import patch
import warnings
import zipfile

import build_offline
import install_host


class OfflineTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.assets = self.root / "assets"
        self.assets.mkdir()
        for name in install_host.ASSET_NAMES:
            (self.assets / name).write_bytes(b"release fixture: " + name.encode())
        self.sums = "".join(f"{install_host.digest(self.assets / name)}  {name}\n"
                            for name in install_host.ASSET_NAMES)
        (self.assets / "SHA256SUMS").write_text(self.sums)
        self.cache = self.root / "cache"
        self.cache.mkdir()
        self.commit = "4" * 40
        self.archive = self.root / "offline.zip"
        # Verify-only must not execute this installer, even on a root machine.
        self.stub = self.root / "stub.py"
        self.stub.write_text("raise RuntimeError('installer must not execute')\n")

    def build(self):
        return build_offline.build(self.assets, self.commit, self.archive, self.stub)

    def verify(self):
        return subprocess.run([sys.executable, "-I", str(self.archive), "--verify-only"],
                              capture_output=True, text=True, timeout=10)

    def rewrite(self, transform):
        with zipfile.ZipFile(self.archive) as bundle:
            entries = [(info, bundle.read(info)) for info in bundle.infolist()]
        with warnings.catch_warnings():
            warnings.simplefilter("ignore", UserWarning)
            with zipfile.ZipFile(self.archive, "w") as bundle:
                for info, data in transform(entries):
                    bundle.writestr(info, data)

    def test_local_stage_and_cached_retry_never_download(self):
        with patch.object(install_host, "download", side_effect=AssertionError("network forbidden")) as download:
            install_host.stage_assets(self.cache, "unused", self.assets)
            install_host.stage_assets(self.cache, "unused", self.cache)
            download.assert_not_called()
        for name in (*install_host.ASSET_NAMES, "SHA256SUMS"):
            self.assertEqual((self.assets / name).read_bytes(), (self.cache / name).read_bytes())

    def test_missing_corrupt_or_symlinked_offline_asset_fails_without_fallback(self):
        path = self.assets / install_host.ASSET_NAMES[-1]
        original = path.read_bytes()
        for fault in ("missing", "corrupt", "symlink"):
            with self.subTest(fault=fault):
                path.unlink()
                if fault == "corrupt":
                    path.write_bytes(b"broken")
                elif fault == "symlink":
                    elsewhere = self.root / "elsewhere"
                    elsewhere.write_bytes(original)
                    path.symlink_to(elsewhere)
                with patch.object(install_host, "download", side_effect=AssertionError("network forbidden")):
                    with self.assertRaises(RuntimeError):
                        install_host.stage_assets(self.cache, "unused", self.assets)
                self.assertEqual(list(self.cache.iterdir()), [])
                path.unlink(missing_ok=True)
                path.write_bytes(original)

    def test_checksum_manifest_rejects_unknown_and_duplicate_names(self):
        for extra in ("../escape", install_host.ASSET_NAMES[0]):
            (self.assets / "SHA256SUMS").write_text(self.sums + f"{'0' * 64}  {extra}\n")
            with self.assertRaises(RuntimeError):
                install_host.read_checksums(self.assets / "SHA256SUMS")

    def test_runtime_keeps_new_offline_installer_over_old_release_copy(self):
        archive = self.root / "runtime.tar.gz"
        with tarfile.open(archive, "w:gz") as bundle:
            for name in sorted(install_host.RUNTIME_FILES):
                info = tarfile.TarInfo(name)
                info.size = len(b"old release")
                bundle.addfile(info, io.BytesIO(b"old release"))
        target = self.root / "installed"
        target.mkdir()
        install_host.install_runtime(archive, target, b"new offline installer")
        self.assertEqual((target / "scripts/install_host.py").read_bytes(), b"new offline installer")
        self.assertEqual((target / "deploy/compose.yaml").read_bytes(), b"old release")

    def test_prepare_and_resume_use_only_local_files_and_leave_caddy_unchanged(self):
        archive = self.assets / "host-files.tar.gz"
        with tarfile.open(archive, "w:gz") as bundle:
            for name in sorted(install_host.RUNTIME_FILES):
                info = tarfile.TarInfo(name)
                info.size = 3
                bundle.addfile(info, io.BytesIO(b"old"))
        (self.assets / "SHA256SUMS").write_text("".join(
            f"{install_host.digest(self.assets / name)}  {name}\n" for name in install_host.ASSET_NAMES))
        installed = self.root / "installed"
        installed.mkdir()
        caddyfile = self.root / "Caddyfile"
        caddyfile.write_text("existing.example.com { respond ok }\n")
        original = caddyfile.read_bytes()
        state = {"commit": self.commit, "domain": "garden.example.com", "caddyfile": str(caddyfile),
                 "subnet": "172.30.21.0/28"}
        (installed / "install-state.json").write_text(json.dumps(state))
        argv = ["--domain", state["domain"], "--commit", self.commit, "--directory", str(installed), "--prepare-only"]
        handles = []
        def local_open(path, *args, **kwargs):
            if path == "/run/lock/little-garden-install.lock":
                handle = builtins.open(self.root / "lock", *args, **kwargs)
                handles.append(handle)
                return handle
            return builtins.open(path, *args, **kwargs)
        with ExitStack() as stack, redirect_stdout(io.StringIO()) as output:
            for name, value in {"os.geteuid": 0, "platform.system": "Linux", "platform.machine": "x86_64",
                                "shutil.which": "/mock", "run": "", "caddy_context": (caddyfile, {})}.items():
                stack.enter_context(patch("install_host." + name, return_value=value))
            stack.enter_context(patch.object(install_host.fcntl, "flock"))
            stack.enter_context(patch.object(install_host.os, "umask"))
            stack.enter_context(patch.object(install_host, "open", side_effect=local_open, create=True))
            stack.enter_context(patch.object(install_host, "download", side_effect=AssertionError("network forbidden")))
            try:
                install_host.main(argv + ["--package-directory", str(self.assets)])
                self.assets.rename(self.root / "unavailable")
                install_host.main(argv)
            finally:
                for handle in handles:
                    handle.close()
        self.assertTrue(json.loads((installed / "install-state.json").read_text())["offline"])
        self.assertIn(str(installed), output.getvalue())
        self.assertIn(b"--package-directory", (installed / "scripts/install_host.py").read_bytes())
        self.assertEqual(caddyfile.read_bytes(), original)
        self.assertFalse((installed / "deploy/.env").exists())

    def test_zip_is_reproducible_and_verify_only_does_not_execute_installer(self):
        first = self.build()
        self.assertEqual(first, self.build())
        result = self.verify()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn(self.commit[:12], result.stdout)

    def test_payload_tampering_is_rejected(self):
        self.build()
        self.rewrite(lambda entries: [(info, b"X" * len(data) if info.filename == "install_host.py" else data)
                                     for info, data in entries])
        result = self.verify()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("校验失败", result.stderr)
        self.assertNotIn("installer must not execute", result.stderr)

    def test_zip_traversal_duplicates_and_links_are_rejected(self):
        for fault in ("traversal", "duplicate", "link"):
            with self.subTest(fault=fault):
                self.build()
                def transform(entries):
                    if fault == "duplicate":
                        return entries + [next(entry for entry in entries if entry[0].filename == "install_host.py")]
                    for info, _ in entries:
                        if info.filename == "install_host.py":
                            if fault == "traversal":
                                info.filename = "../escape.py"
                            else:
                                info.external_attr = (stat.S_IFLNK | 0o777) << 16
                    return entries
                self.rewrite(transform)
                result = self.verify()
                self.assertNotEqual(result.returncode, 0, result.stdout)
                self.assertNotIn("installer must not execute", result.stderr)
                self.assertFalse((self.root / "escape.py").exists())

    def test_builder_rejects_damaged_release_before_creating_output(self):
        (self.assets / install_host.ASSET_NAMES[0]).write_bytes(b"broken")
        with self.assertRaises(RuntimeError):
            self.build()
        self.assertFalse(self.archive.exists())


if __name__ == "__main__":
    unittest.main()
