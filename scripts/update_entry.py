"""Executable offline update ZIP template; keeps the installed runtime and keys."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import stat
import subprocess
import sys
import tempfile
import zipfile

COMMIT = "__APPLICATION_COMMIT__"
IMAGE = "little-garden-linux-amd64.tar.gz"
PAYLOAD = {}
RUNTIME_HASHES = {}
RUNTIME_FILES = {"deploy/compose.yaml", "scripts/deploy.sh", "scripts/backup.sh"}


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def verify_image(archive, directory):
    require(re.fullmatch(r"[a-f0-9]{40}", COMMIT), "更新包版本无效。")
    with zipfile.ZipFile(archive) as bundle:
        entries = bundle.infolist()
        require(len(entries) == 2 and {item.filename for item in entries} == {"__main__.py", IMAGE},
                "更新包文件清单不匹配。")
        require(all(not item.is_dir() and stat.S_IFMT(item.external_attr >> 16) in (0, stat.S_IFREG)
                    for item in entries), "更新包含有非普通文件。")
        item = bundle.getinfo(IMAGE)
        require(item.file_size == PAYLOAD["size"] and item.file_size <= 256 * 1024 * 1024,
                "镜像大小不匹配。")
        content = bundle.read(item)
        require(hashlib.sha256(content).hexdigest() == PAYLOAD["sha256"], "镜像校验失败。")
        path = directory / IMAGE
        path.write_bytes(content)
        return path


def check_installation(root):
    require(root.is_absolute() and root.is_dir() and not any(p.is_symlink() for p in (root, *root.parents)),
            "安装目录无效。")
    require(set(RUNTIME_HASHES) == RUNTIME_FILES, "更新包的运行文件清单无效。")
    for name, expected in RUNTIME_HASHES.items():
        path = root / name
        require(path.is_file() and not path.is_symlink() and not path.parent.is_symlink(),
                f"缺少运行文件：{name}。")
        require(hashlib.sha256(path.read_bytes()).hexdigest() == expected,
                f"服务器上的 {name} 与本次更新不兼容，未更改服务。")
    env = root / "deploy/.env"
    require(env.is_file() and not env.is_symlink(), "未找到已配置的 deploy/.env。")
    state = root / ".local/deploy/current"
    require(state.is_file() and re.fullmatch(r"sha256:[a-f0-9]{64}", state.read_text().strip()),
            "未找到已有部署记录；此包仅用于更新已经安装的花园。")


def apply_update(root, image_file):
    check_installation(root)
    subprocess.run(["docker", "compose", "--project-directory", str(root / "deploy"),
                    "-f", str(root / "deploy/compose.yaml"), "config", "--quiet"], check=True)
    print("加载离线镜像……", flush=True)
    subprocess.run(["docker", "load", "--input", str(image_file)], check=True)
    version = f"v0.1.0-preview.{COMMIT[:12]}"
    image = f"ghcr.io/ieuforu/little-garden:{version}"
    result = subprocess.run(["docker", "image", "inspect", image],
                            capture_output=True, text=True, check=True)
    metadata = json.loads(result.stdout)[0]
    require(metadata.get("Os") == "linux" and metadata.get("Architecture") == "amd64"
            and ((metadata.get("Config") or {}).get("Labels") or {}).get("org.opencontainers.image.revision") == COMMIT,
            "镜像架构或版本不匹配；未切换服务。")
    print("备份已有花田并更新；等待健康检查……", flush=True)
    # Existing deploy.sh owns locking, backup, health checks and compatible rollback.
    subprocess.run(["bash", str(root / "scripts/deploy.sh"), "--local", version], check=True)
    print("更新完成，刷新花园页面即可。", flush=True)


def main():
    parser = argparse.ArgumentParser(description="Little Garden 离线更新（保留已有配置和花田）")
    parser.add_argument("--directory", type=Path, default=Path("/srv/little-garden"))
    parser.add_argument("--verify-only", action="store_true")
    args = parser.parse_args()
    with tempfile.TemporaryDirectory(prefix="garden-update-") as temporary:
        image = verify_image(sys.argv[0], Path(temporary))
        print(f"更新包校验通过：{COMMIT[:12]}", flush=True)
        if args.verify_only:
            return
        require(os.geteuid() == 0, "请使用 sudo python3 -I 执行更新。")
        require(platform.system() == "Linux" and platform.machine() in {"x86_64", "amd64"},
                "此更新包只支持 Linux amd64。")
        apply_update(args.directory, image)


if __name__ == "__main__":
    try:
        main()
    except (Exception, KeyboardInterrupt) as error:
        print(f"更新停止：{error}", file=sys.stderr)
        sys.exit(1)
