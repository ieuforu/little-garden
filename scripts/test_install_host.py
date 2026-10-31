import importlib.util
import io
import os
from pathlib import Path
import tarfile
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("installer", Path(__file__).with_name("install_host.py"))
installer = importlib.util.module_from_spec(spec)
spec.loader.exec_module(installer)


class HostInstallerTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.config = self.root / "Caddyfile"
        self.original = b"existing.example.com {\n    reverse_proxy localhost:3000\n}\n"
        self.config.write_bytes(self.original)
        self.config.chmod(0o640)
        self.backups = self.root / "backups"

    def test_validation_failure_leaves_other_sites_and_file_permissions_unchanged(self):
        calls = []
        def command(*args):
            calls.append(args)
            raise RuntimeError("validation rejected")
        with self.assertRaises(RuntimeError):
            installer.apply_caddy(self.config, "garden.example.com", self.backups, command)
        self.assertEqual(self.config.read_bytes(), self.original)
        self.assertEqual(self.config.stat().st_mode & 0o777, 0o640)
        self.assertFalse(self.backups.exists())
        self.assertEqual([call[1] for call in calls], ["validate"])

    def test_failed_reload_restores_disk_and_reloads_original_config(self):
        observed = []
        def command(*args):
            if args[1] == "reload":
                observed.append(self.config.read_bytes())
                if len(observed) == 1:
                    raise RuntimeError("new config failed")
        with self.assertRaisesRegex(RuntimeError, "原文件和配置已恢复"):
            installer.apply_caddy(self.config, "garden.example.com", self.backups, command)
        self.assertIn(b"garden.example.com", observed[0])
        self.assertEqual(observed[1], self.original)
        self.assertEqual(self.config.read_bytes(), self.original)
        self.assertEqual(next(self.backups.iterdir()).read_bytes(), self.original)

    def test_operator_edit_is_not_overwritten_after_validation(self):
        def command(*args):
            self.config.write_bytes(b"operator change\n")
        with self.assertRaisesRegex(RuntimeError, "其他程序修改"):
            installer.apply_caddy(self.config, "garden.example.com", self.backups, command)
        self.assertEqual(self.config.read_bytes(), b"operator change\n")

    def test_operator_edit_is_not_overwritten_during_failed_reload_recovery(self):
        def command(*args):
            if args[1] == "reload":
                self.config.write_bytes(b"operator during reload\n")
                raise RuntimeError("reload failed")
        with self.assertRaisesRegex(RuntimeError, "未覆盖"):
            installer.apply_caddy(self.config, "garden.example.com", self.backups, command)
        self.assertEqual(self.config.read_bytes(), b"operator during reload\n")

    def test_success_preserves_existing_sites_and_is_idempotent(self):
        calls = []
        command = lambda *args: calls.append(args)
        installer.apply_caddy(self.config, "garden.example.com", self.backups, command)
        self.assertTrue(self.config.read_bytes().startswith(self.original))
        self.assertEqual(self.config.stat().st_mode & 0o777, 0o640)
        first = self.config.read_bytes()
        installer.apply_caddy(self.config, "garden.example.com", self.backups, command)
        self.assertEqual(self.config.read_bytes(), first)
        self.assertEqual([call[1] for call in calls], ["validate", "reload"])

    def test_existing_managed_block_for_different_domain_is_not_replaced(self):
        data = self.original + installer.site_block("first.example.com").encode()
        with self.assertRaises(RuntimeError):
            installer.updated_config(data, "second.example.com")

    def test_untrusted_domain_and_dotenv_injection_are_rejected(self):
        for domain in ("https://example.com", "example.com\nadmin off", "example.com; id", "127.0.0.1", "../config"):
            with self.assertRaises(RuntimeError):
                installer.domain_name(domain)
        path = self.root / ".env"
        with self.assertRaises(RuntimeError):
            installer.write_env(path, "garden.example.com", "172.30.21.0/28", "a" * 30, "secret\nGARDEN_MODE=development")
        self.assertFalse(path.exists())

    def test_secret_file_is_private_and_never_overwritten(self):
        path = self.root / ".env"
        installer.write_env(path, "garden.example.com", "172.30.21.0/28", "a" * 30, "b" * 30)
        before = path.read_bytes()
        self.assertEqual(path.stat().st_mode & 0o777, 0o600)
        with self.assertRaises(RuntimeError):
            installer.write_env(path, "garden.example.com", "172.30.21.0/28", "c" * 30, "d" * 30)
        self.assertEqual(path.read_bytes(), before)

    def test_subnet_selection_avoids_host_and_docker_networks(self):
        self.assertEqual(installer.select_subnet(["172.30.21.0/24", "172.30.22.0/28", "172.24.0.0/16"]), "172.30.23.0/28")
        with self.assertRaises(RuntimeError):
            installer.select_subnet(["172.16.0.0/12"])

    def test_reads_active_config_path_without_assuming_etc_caddy(self):
        self.assertEqual(installer.config_from_args(["caddy", "run", "--config", "/srv/proxy/Caddyfile"], "/"), Path("/srv/proxy/Caddyfile"))
        self.assertEqual(installer.config_from_args(["caddy", "run", "--config=Caddyfile"], "/srv/proxy"), Path("/srv/proxy/Caddyfile"))
        for args in (["caddy", "run", "--resume"], ["caddy", "run", "--config", "/srv/caddy.json"]):
            with self.assertRaises(RuntimeError):
                installer.config_from_args(args, "/")

    def test_nested_wildcard_hostname_collision_is_detected(self):
        config = {"apps": {"http": {"routes": [{"match": [{"host": ["*.example.com"]}]}]}}}
        self.assertTrue(installer.host_matches(config, "garden.example.com"))
        self.assertFalse(installer.host_matches(config, "garden.other.com"))

    def test_archive_path_escape_and_links_are_rejected_before_any_extraction(self):
        for bad_name, link in (("../escaped", False), ("scripts/install_host.py", True)):
            archive = self.root / "bundle.tar.gz"
            with tarfile.open(archive, "w:gz") as bundle:
                names = sorted(installer.RUNTIME_FILES)
                for name in names:
                    entry = tarfile.TarInfo(bad_name if name == "scripts/install_host.py" else name)
                    if name == "scripts/install_host.py" and link:
                        entry.type = tarfile.SYMTYPE
                        entry.linkname = "/etc/passwd"
                        bundle.addfile(entry)
                    else:
                        entry.size = 4
                        bundle.addfile(entry, io.BytesIO(b"test"))
            target = self.root / "unpack"
            target.mkdir(exist_ok=True)
            with self.assertRaises(RuntimeError):
                installer.extract_runtime(archive, target)
            self.assertEqual(list(target.iterdir()), [])


if __name__ == "__main__":
    unittest.main()
