#!/usr/bin/env python3
"""Package existing CI release assets into one executable ZIP. Never downloads."""

import argparse
import hashlib
from pathlib import Path
import re
import stat
import zipfile

from install_host import ASSET_NAMES, digest, read_checksums, require


def build(assets, commit, output, installer=None):
    require(re.fullmatch(r"[a-f0-9]{40}", commit), "A full application commit is required")
    checksums = read_checksums(assets / "SHA256SUMS")
    for name in ASSET_NAMES:
        path = assets / name
        require(path.is_file() and not path.is_symlink() and path.stat().st_size <= 256 * 1024 * 1024,
                f"Invalid release asset: {name}")
        require(digest(path) == checksums[name], f"Release checksum mismatch: {name}")
    installer = installer or Path(__file__).with_name("install_host.py")
    contents = {name: (assets / name).read_bytes() for name in (*ASSET_NAMES, "SHA256SUMS")}
    contents["install_host.py"] = installer.read_bytes()
    manifest = {name: {"sha256": hashlib.sha256(data).hexdigest(), "size": len(data)}
                for name, data in contents.items()}
    entry = Path(__file__).with_name("offline_entry.py").read_text()
    entry = entry.replace("__APPLICATION_COMMIT__", commit).replace("PAYLOADS = {}", f"PAYLOADS = {manifest!r}")
    contents["__main__.py"] = entry.encode()
    output.parent.mkdir(parents=True, exist_ok=True)
    # Fixed metadata makes the outer SHA reproducible. Payloads are already gzip.
    with zipfile.ZipFile(output, "w", compression=zipfile.ZIP_STORED) as bundle:
        for name, data in sorted(contents.items()):
            info = zipfile.ZipInfo(name, date_time=(2026, 1, 1, 0, 0, 0))
            info.create_system = 3
            info.external_attr = (stat.S_IFREG | 0o644) << 16
            bundle.writestr(info, data)
    return digest(output)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--assets", type=Path, required=True)
    parser.add_argument("--commit", required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    print(f"{build(args.assets, args.commit, args.output)}  {args.output}")
