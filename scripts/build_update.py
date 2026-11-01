#!/usr/bin/env python3
"""Build an offline image-only update from CI assets. Does not download."""

import argparse
import hashlib
from pathlib import Path
import re
import stat
import tarfile
import zipfile

from install_host import ASSET_NAMES, digest, read_checksums, require
from update_entry import IMAGE, RUNTIME_FILES


def build(assets, commit, output):
    require(re.fullmatch(r"[a-f0-9]{40}", commit), "A full application commit is required")
    checksums = read_checksums(assets / "SHA256SUMS")
    for name in ASSET_NAMES:
        path = assets / name
        require(path.is_file() and not path.is_symlink() and path.stat().st_size <= 256 * 1024 * 1024,
                f"Invalid release asset: {name}")
        require(digest(path) == checksums[name], f"Release checksum mismatch: {name}")
    runtime = {}
    with tarfile.open(assets / "host-files.tar.gz", "r:gz") as bundle:
        for name in sorted(RUNTIME_FILES):
            matches = [member for member in bundle.getmembers() if member.name == name]
            require(len(matches) == 1 and matches[0].isfile() and matches[0].size < 256 * 1024,
                    f"Invalid runtime file: {name}")
            runtime[name] = hashlib.sha256(bundle.extractfile(matches[0]).read()).hexdigest()
    image = (assets / IMAGE).read_bytes()
    payload = {"sha256": hashlib.sha256(image).hexdigest(), "size": len(image)}
    entry = Path(__file__).with_name("update_entry.py").read_text()
    entry = entry.replace("__APPLICATION_COMMIT__", commit).replace("PAYLOAD = {}", f"PAYLOAD = {payload!r}")
    entry = entry.replace("RUNTIME_HASHES = {}", f"RUNTIME_HASHES = {runtime!r}")
    output.parent.mkdir(parents=True, exist_ok=True)
    with zipfile.ZipFile(output, "w", compression=zipfile.ZIP_STORED) as bundle:
        for name, data in sorted({"__main__.py": entry.encode(), IMAGE: image}.items()):
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
