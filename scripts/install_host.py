#!/usr/bin/env python3
"""First installation on an amd64 Linux host with an existing systemd Caddy.

Run as root, with an exact application commit. --package-directory uses local
release files with no download fallback. --prepare-only verifies the release
without starting the app or changing Caddy. No third-party Python
packages, server-side compilation, DNS edits or Docker registry login required.
"""

import argparse
import fcntl
import fnmatch
import getpass
import hashlib
import ipaddress
import json
import os
from pathlib import Path
import platform
import re
import secrets
import shlex
import shutil
import socket
import stat
import subprocess
import sys
import tarfile
import tempfile
import time
import urllib.request

REPO = "ieuforu/little-garden"
ASSET_NAMES = ("host-files.tar.gz", "little-garden-linux-amd64.tar.gz")
RUNTIME_FILES = {
    "deploy/compose.yaml", "scripts/deploy.sh", "scripts/backup.sh",
    "scripts/install_host.py",
}
BEGIN = "# BEGIN little-garden managed site"
END = "# END little-garden managed site"


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def run(*args, timeout=60):
    result = subprocess.run(args, capture_output=True, text=True, timeout=timeout)
    # Existing Caddy config/environment may contain secrets. Do not echo command
    # output on failure, or include it in an exception/terminal screenshot.
    require(result.returncode == 0, f"命令失败：{args[0]} {args[1] if len(args) > 1 else ''}（退出码 {result.returncode}）。已停止。")
    return result.stdout.strip()


def domain_name(value):
    value = value.lower().strip()
    labels = value.split(".")
    require(len(value) <= 253 and len(labels) >= 2 and all(
        re.fullmatch(r"[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?", label)
        for label in labels
    ), "域名格式无效；只填写主机名，不包含 https:// 或路径。")
    require(not value.replace(".", "").isdigit(), "请使用域名，不使用 IP 地址。")
    return value


def atomic_write(path, content, mode=0o600, owner=None):
    path = Path(path)
    fd, name = tempfile.mkstemp(prefix=".garden-", dir=path.parent)
    try:
        with os.fdopen(fd, "wb") as stream:
            stream.write(content)
            stream.flush()
            os.fsync(stream.fileno())
            os.fchmod(stream.fileno(), mode)
            if owner is not None:
                os.fchown(stream.fileno(), *owner)
        os.replace(name, path)
    finally:
        Path(name).unlink(missing_ok=True)


def digest(path):
    hasher = hashlib.sha256()
    with open(path, "rb") as stream:
        for part in iter(lambda: stream.read(1024 * 1024), b""):
            hasher.update(part)
    return hasher.hexdigest()


def download(url, destination, expected=None, max_bytes=256 * 1024 * 1024):
    destination = Path(destination)
    if expected and destination.is_file() and digest(destination) == expected:
        return
    temporary = destination.with_suffix(destination.suffix + ".part")
    try:
        run("curl", "--fail", "--location", "--silent", "--show-error",
            "--proto", "=https", "--proto-redir", "=https",
            "--connect-timeout", "15", "--max-time", "240", "--retry", "2",
            "--max-filesize", str(max_bytes), "--output", str(temporary), url,
            timeout=750)
        require(temporary.stat().st_size <= max_bytes, "下载文件超出预期大小。")
        if expected:
            require(digest(temporary) == expected, "下载文件的 SHA-256 不匹配；未执行该文件。")
        os.replace(temporary, destination)
    finally:
        temporary.unlink(missing_ok=True)


def extract_runtime(archive, target):
    # Do not trust tar paths, links, permissions or owners, even in release files.
    with tarfile.open(archive, "r:gz") as bundle:
        members = bundle.getmembers()
        require(len(members) == len(RUNTIME_FILES) and
                {m.name for m in members} == RUNTIME_FILES,
                "安装包的文件列表不匹配。")
        require(all(m.isfile() and m.size <= 256 * 1024 for m in members),
                "安装包包含非普通文件或过大的文件。")
        for member in members:
            output = target / member.name
            output.parent.mkdir(mode=0o750, parents=True, exist_ok=True)
            require(not output.is_symlink() and not output.parent.is_symlink(),
                    "安装目录中存在符号链接；已停止。")
            with bundle.extractfile(member) as stream:
                atomic_write(output, stream.read(), 0o755 if member.name.endswith(".sh") else 0o644)


def read_checksums(path):
    require(path.is_file() and not path.is_symlink() and path.stat().st_size <= 16 * 1024,
            "缺少有效的 SHA256SUMS 文件。")
    checksums = {}
    for line in path.read_text().splitlines():
        parts = line.split()
        require(len(parts) == 2 and re.fullmatch(r"[a-f0-9]{64}", parts[0]), "校验文件格式无效。")
        name = parts[1].lstrip("*")
        require(name in ASSET_NAMES and name not in checksums, "校验文件包含未知或重复文件。")
        checksums[name] = parts[0]
    require(set(checksums) == set(ASSET_NAMES), "校验文件缺少必要的安装包。")
    return checksums


def stage_assets(cache, version, package_directory=None):
    if package_directory is None:
        base = f"https://github.com/{REPO}/releases/download/{version}"
        download(base + "/SHA256SUMS", cache / "SHA256SUMS", max_bytes=16 * 1024)
        checksums = read_checksums(cache / "SHA256SUMS")
        for name in ASSET_NAMES:
            download(base + "/" + name, cache / name, checksums[name])
        return
    source = Path(package_directory)
    checksums = read_checksums(source / "SHA256SUMS")
    # Validate every local payload before writing anything. Missing or damaged
    # offline files must fail immediately, never fall back to GitHub.
    for name in ASSET_NAMES:
        path = source / name
        require(path.is_file() and not path.is_symlink() and path.stat().st_size <= 256 * 1024 * 1024,
                f"离线包文件缺失或格式无效：{name}。")
        require(digest(path) == checksums[name], f"离线包 SHA-256 不匹配：{name}。")
    if source.resolve() != cache.resolve():
        for name in (*ASSET_NAMES, "SHA256SUMS"):
            atomic_write(cache / name, (source / name).read_bytes())


def install_runtime(archive, root, bootstrap):
    extract_runtime(archive, root)
    # Transport fixes can be newer than the pinned application image. Keep the
    # verified bootstrap so a later retry retains offline support.
    atomic_write(root / "scripts/install_host.py", bootstrap, 0o644)


def select_subnet(occupied):
    networks = [ipaddress.ip_network(item, strict=False) for item in occupied]
    for third in range(21, 250):
        candidate = ipaddress.ip_network(f"172.30.{third}.0/28")
        if not any(n.version == 4 and candidate.overlaps(n) for n in networks):
            return str(candidate)
    raise RuntimeError("未找到空闲的专用 Docker 子网，请手动选择。")


def available_subnet():
    ids = run("docker", "network", "ls", "--quiet").splitlines()
    existing = json.loads(run("docker", "network", "inspect", *ids)) if ids else []
    occupied = [c["Subnet"] for n in existing for c in n.get("IPAM", {}).get("Config", []) if c.get("Subnet")]
    for route in json.loads(run("ip", "-json", "route", "show", "table", "all")):
        dst = route.get("dst", "default")
        if dst not in ("default", "0.0.0.0/0"):
            occupied.append(dst)
    return select_subnet(occupied)


def config_from_args(args, working_directory):
    require("--resume" not in args, "Caddy 使用 --resume 启动，不能自动修改磁盘配置。")
    config = None
    adapter = "caddyfile"
    for index, arg in enumerate(args):
        if arg in ("--config", "-c") and index + 1 < len(args):
            config = args[index + 1]
        elif arg.startswith("--config="):
            config = arg.split("=", 1)[1]
        elif arg in ("--adapter", "-a") and index + 1 < len(args):
            adapter = args[index + 1]
        elif arg.startswith("--adapter="):
            adapter = arg.split("=", 1)[1]
    require(config and config != "-" and adapter == "caddyfile" and not config.endswith(".json"),
            "无法自动确定正在使用的 Caddyfile；请提供 Caddy 启动配置。")
    path = Path(config)
    return (path if path.is_absolute() else Path(working_directory) / path).resolve()


def caddy_context():
    require(run("systemctl", "is-active", "caddy") == "active", "caddy.service 未运行。")
    pid = run("systemctl", "show", "caddy", "--property=MainPID", "--value")
    require(pid.isdigit() and int(pid) > 1, "无法确认 Caddy 主进程。")
    proc = Path("/proc") / pid
    args = (proc / "cmdline").read_bytes().decode().strip("\0").split("\0")
    config = config_from_args(args, os.readlink(proc / "cwd"))
    require(config.is_file(), "运行中的 Caddyfile 不存在。")
    adapted = json.loads(run("caddy", "adapt", "--config", str(config), "--adapter", "caddyfile"))
    admin = adapted.get("admin", {})
    require(not admin.get("disabled") and admin.get("listen", "localhost:2019") in
            ("localhost:2019", "127.0.0.1:2019"),
            "此安装器仅自动处理本机默认 Caddy 管理接口；现有配置未改动。")
    # Never use a configured HTTP proxy for the loopback admin request.
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    with opener.open("http://127.0.0.1:2019/config/", timeout=5) as response:
        live = json.loads(response.read(8 * 1024 * 1024))
    require(live == adapted, "Caddy 当前运行配置与磁盘文件不同；为保留已有改动，安装已停止。")
    return config, live


def host_matches(config, domain):
    if isinstance(config, dict):
        for pattern in config.get("host", []) if isinstance(config.get("host"), list) else []:
            if isinstance(pattern, str) and fnmatch.fnmatchcase(domain, pattern):
                return True
        return any(host_matches(value, domain) for value in config.values())
    if isinstance(config, list):
        return any(host_matches(value, domain) for value in config)
    return False


def site_block(domain):
    return f"""{BEGIN}
{domain} {{
    encode zstd gzip
    request_body {{
        max_size 8KB
    }}
    reverse_proxy 127.0.0.1:18080 {{
        header_up X-Real-IP {{http.request.remote.host}}
    }}
}}
{END}
"""


def updated_config(original, domain):
    text = original.decode("utf-8")
    block = site_block(domain)
    if BEGIN in text or END in text:
        require(text.count(BEGIN) == 1 and text.count(END) == 1 and block in text,
                "已有 little-garden 配置与本次安装不同，已停止自动修改。")
        return original
    return original + b"\n\n" + block.encode()


def apply_caddy(path, domain, backup_dir, command=run):
    original = path.read_bytes()
    updated = updated_config(original, domain)
    if updated == original:
        return
    info = path.stat()
    fd, temporary = tempfile.mkstemp(prefix=".garden-candidate-", dir=path.parent)
    candidate = Path(temporary)
    try:
        with os.fdopen(fd, "wb") as stream:
            stream.write(updated)
        command("caddy", "validate", "--config", str(candidate), "--adapter", "caddyfile")
        require(path.read_bytes() == original, "Caddyfile 在检查期间被其他程序修改；已停止。")
        backup_dir.mkdir(mode=0o700, parents=True, exist_ok=True)
        backup = backup_dir / f"Caddyfile-before-{time.time_ns()}"
        atomic_write(backup, original)
        atomic_write(path, updated, stat.S_IMODE(info.st_mode), (info.st_uid, info.st_gid))
        try:
            command("caddy", "reload", "--config", str(path), "--adapter", "caddyfile")
        except Exception as error:
            # Do not overwrite a concurrent operator edit during recovery.
            if path.read_bytes() == updated:
                atomic_write(path, original, stat.S_IMODE(info.st_mode), (info.st_uid, info.st_gid))
                try:
                    command("caddy", "reload", "--config", str(path), "--adapter", "caddyfile")
                except Exception:
                    raise RuntimeError(f"Caddy 文件已恢复，但重载未确认。原配置备份：{backup}") from error
                raise RuntimeError("新 Caddy 配置重载失败，原文件和配置已恢复。") from error
            raise RuntimeError(f"Caddyfile 被其他程序修改，未覆盖；原配置备份：{backup}") from error
    finally:
        candidate.unlink(missing_ok=True)


def write_env(path, domain, subnet, site_key, secret_key):
    for value in (site_key, secret_key):
        require(re.fullmatch(r"[A-Za-z0-9_-]{16,200}", value) and
                not value.startswith(("1x000000", "2x000000", "3x000000")),
                "请输入真实 Turnstile 密钥；测试密钥、空值或多行内容不允许用于上线。")
    require(not path.exists(), "配置文件已经存在，拒绝覆盖已有密钥。")
    content = (f"PUBLIC_ORIGIN=https://{domain}\nSESSION_SECRET={secrets.token_hex(32)}\n"
               f"TURNSTILE_SITE_KEY={site_key}\nTURNSTILE_SECRET_KEY={secret_key}\n"
               f"TRUSTED_PROXIES={subnet}\nGARDEN_SUBNET={subnet}\n"
               "GARDEN_PORT=18080\nMAX_WEBSOCKETS=100\n")
    atomic_write(path, content.encode())


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--domain", required=True)
    parser.add_argument("--commit", required=True)
    parser.add_argument("--directory", default="/srv/little-garden")
    parser.add_argument("--prepare-only", action="store_true")
    parser.add_argument("--package-directory", type=Path)
    args = parser.parse_args(argv)
    bootstrap = Path(__file__).read_bytes()
    domain = domain_name(args.domain)
    require(re.fullmatch(r"[a-f0-9]{40}", args.commit), "必须提供完整的 40 位源码 commit。")
    require(os.geteuid() == 0, "请通过 sudo python3 运行此安装器。")
    require(platform.system() == "Linux" and platform.machine() == "x86_64", "此安装包只支持 Linux amd64。")
    for binary in ("docker", "caddy", "systemctl", "curl", "ip", "bash", "flock"):
        require(shutil.which(binary), f"缺少命令：{binary}。")
    os.umask(0o077)
    root = Path(args.directory).absolute()
    require(not root.is_symlink() and root.parent.is_dir(), "安装目录必须位于现有目录内，且不能是符号链接。")
    lock = open("/run/lock/little-garden-install.lock", "a")
    fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
    run("docker", "info", "--format", "{{.ServerVersion}}")
    run("docker", "compose", "version")
    caddyfile, live = caddy_context()
    state_path = root / "install-state.json"
    if root.exists():
        require(state_path.is_file() and not state_path.is_symlink(), f"{root} 已存在且不是本安装器管理的目录；未覆盖。")
        state = json.loads(state_path.read_text())
        require(state.get("commit") == args.commit and state.get("domain") == domain and
                state.get("caddyfile") == str(caddyfile), "已有安装与本次参数不同；更新请使用 scripts/deploy.sh。")
    else:
        require(not run("docker", "ps", "--all", "--quiet", "--filter", "label=com.docker.compose.project=little-garden"),
                "发现已有 little-garden 容器；首次安装器不会接管它。")
        require(not run("docker", "volume", "ls", "--quiet", "--filter", "label=com.docker.compose.project=little-garden"),
                "发现已有 little-garden 数据卷；请先核实数据归属。")
        require(not host_matches(live, domain), "Caddy 已有匹配此域名的站点；请先核实，未替换。")
        with socket.socket() as probe:
            probe.bind(("127.0.0.1", 18080))
        state = {"commit": args.commit, "domain": domain, "caddyfile": str(caddyfile), "subnet": available_subnet()}
        root.mkdir(mode=0o750)
        atomic_write(state_path, (json.dumps(state, indent=2) + "\n").encode())
    # A matching domain is only accepted on a retry if our exact managed block exists.
    if host_matches(live, domain):
        require(site_block(domain) in caddyfile.read_text(), "域名已被其他 Caddy 配置使用，已停止。")
    version = f"v0.1.0-preview.{args.commit[:12]}"
    cache = root / ".cache"
    cache.mkdir(mode=0o700, exist_ok=True)
    local_package = args.package_directory or (cache if state.get("offline") else None)
    print(f"Caddyfile：{caddyfile}\n安装目录：{root}", flush=True)
    print("使用离线镜像，不访问 GitHub 或镜像仓库。" if local_package else
          "下载已构建的镜像，无需在服务器编译。", flush=True)
    stage_assets(cache, version, local_package)
    install_runtime(cache / ASSET_NAMES[0], root, bootstrap)
    state["version"] = version
    state["offline"] = local_package is not None
    atomic_write(state_path, (json.dumps(state, indent=2) + "\n").encode())
    if args.prepare_only:
        print("准备完成：安装包 SHA-256 已验证。尚未启动服务，也未修改 Caddy。")
        activation = shlex.join(["sudo", "python3", str(root / "scripts/install_host.py"), "--domain", domain,
                                 "--commit", args.commit, "--directory", str(root)])
        print(f"准备好 Turnstile 密钥后执行：\n{activation}")
        return
    envfile = root / "deploy/.env"
    if not envfile.exists():
        print(f"Turnstile：请先创建 Managed widget，允许主机名 {domain}；不要开启预清除。")
        # Read both values directly from the terminal. Neither enters shell history,
        # a command line, a GitHub artifact, nor assistant-visible chat output.
        with open("/dev/tty", "r") as tty_in, open("/dev/tty", "w") as tty_out:
            tty_out.write("Site key：")
            tty_out.flush()
            site_key = tty_in.readline().strip()
            require(site_key, "未输入 Site key；准备好的安装文件已保留。")
            secret_key = getpass.getpass("Secret key（输入不显示）：", stream=tty_out).strip()
        write_env(envfile, domain, state["subnet"], site_key, secret_key)
    else:
        require(not envfile.is_symlink(), "配置文件不能是符号链接。")
        values = dict(line.split("=", 1) for line in envfile.read_text().splitlines() if "=" in line and not line.startswith("#"))
        require(values.get("PUBLIC_ORIGIN") == f"https://{domain}" and values.get("GARDEN_SUBNET") == state["subnet"],
                "现有应用配置与安装记录不一致，已停止。")
        os.chmod(envfile, 0o600)
    print("加载镜像并启动花园……", flush=True)
    run("docker", "load", "--input", str(cache / ASSET_NAMES[1]), timeout=180)
    image = f"ghcr.io/{REPO}:{version}"
    info = json.loads(run("docker", "image", "inspect", image))[0]
    require(info.get("Os") == "linux" and info.get("Architecture") == "amd64" and
            info.get("Config", {}).get("Labels", {}).get("org.opencontainers.image.revision") == args.commit,
            "镜像的架构或源码版本不匹配；未启动。")
    run("bash", str(root / "scripts/deploy.sh"), "--local", version, timeout=180)
    # Recheck the live/disk match immediately before adding the route.
    current, live = caddy_context()
    require(current == caddyfile, "Caddy 启动配置在安装期间改变；未添加站点。")
    if host_matches(live, domain):
        require(site_block(domain) in caddyfile.read_text(), "域名在安装期间被其他站点使用；未覆盖。")
    apply_caddy(caddyfile, domain, root / "backups")
    print("花园容器健康，Caddy 已加载站点。正在核验 HTTPS……", flush=True)
    for _ in range(12):
        result = subprocess.run(["curl", "--fail", "--silent", "--noproxy", "*", "--connect-timeout", "2", "--max-time", "3",
                                 "--resolve", f"{domain}:443:127.0.0.1", f"https://{domain}/readyz"], capture_output=True)
        if result.returncode == 0 and result.stdout.strip() == b'{"status":"ready"}':
            print(f"部署完成，HTTPS 健康检查通过：https://{domain}")
            print("还需用手机实际种一朵花，验证真实 Turnstile 和浏览器交互。")
            return
        time.sleep(2)
    print("服务已启动，但 HTTPS 尚未验证成功；请检查域名解析和 Caddy 证书签发日志。")
    raise RuntimeError("HTTPS 验证未完成；现有配置和数据库已保留，没有重启其他服务。")


if __name__ == "__main__":
    try:
        main()
    except (Exception, KeyboardInterrupt) as error:
        print(f"安装停止：{error}", file=sys.stderr)
        sys.exit(1)
