#!/usr/bin/env python3
"""Install the pinned upstream binary into an explicit local directory."""
import hashlib
import io
import pathlib
import platform
import sys
import tarfile
import urllib.request

VERSION = "2.14.2"
# Upstream release .sha512 files, checked against the v2.14.2 release assets.
CHECKSUMS = {
    "aarch64-apple-darwin": "6ec30660233fcd73b92e9d888965945b6435d6fcd7120df8d7c0799faa46cf06d563d49de721ccf4387e6d61bdc5b37c8eb73273c7d9a0883b90cd9857c6ad9f",
    "x86_64-apple-darwin": "4d32c50a010cbff65e46f1f36347c2dd1a20f17efea7b77115d3166146ad467dc803cd07b839fc35e0a6639aa8d1af9eab7cf8f7984bcd819513f46a396ff801",
    "x86_64-unknown-linux-gnu": "26d1f7c8ea2400f2ccb0b2e7f321635f600b583bf4cd1f7afaa5e8c24068ad1088a2504e0a4d9f4333d4bb20a80329327d9462634343ee3af91e1a7fbdc6f18a",
    "aarch64-unknown-linux-gnu": "fd27768ea76fea8c3ba3f3247fef2f6604988b629f15e48bf40be5eaa08b8fef2e4087cc9c8af16bc522bc19cc369611fa7692101b513f63469ad58522708600",
}


def install(directory):
    arch = {"arm64": "aarch64", "aarch64": "aarch64", "x86_64": "x86_64"}[platform.machine()]
    target = arch + {"Darwin": "-apple-darwin", "Linux": "-unknown-linux-gnu"}[platform.system()]
    name = f"git-cliff-{VERSION}-{target}.tar.gz"
    url = f"https://github.com/orhun/git-cliff/releases/download/v{VERSION}/{name}"
    with urllib.request.urlopen(url, timeout=60) as response:
        data = response.read()
    if hashlib.sha512(data).hexdigest() != CHECKSUMS[target]:
        raise ValueError("git-cliff checksum mismatch")
    with tarfile.open(fileobj=io.BytesIO(data), mode="r:gz") as archive:
        members = [m for m in archive.getmembers() if pathlib.PurePosixPath(m.name).name == "git-cliff" and m.isfile()]
        if len(members) != 1:
            raise ValueError("expected exactly one git-cliff binary")
        binary = archive.extractfile(members[0]).read()
    directory = pathlib.Path(directory)
    directory.mkdir(parents=True, exist_ok=True)
    output = directory / "git-cliff"
    output.write_bytes(binary)
    output.chmod(0o755)
    print(output.resolve())


if __name__ == "__main__":
    if len(sys.argv) != 2:
        sys.exit("usage: python3 scripts/install-git-cliff.py OUTPUT_DIRECTORY")
    install(sys.argv[1])
