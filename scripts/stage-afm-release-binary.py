#!/usr/bin/env python3
"""Stage one verified AFM executable from the published release archives."""
import hashlib
import json
import pathlib
import sys
import tarfile


def stage(archives, platform, arch, output):
    if platform not in {"darwin", "linux"} or arch not in {"amd64", "arm64"}:
        raise ValueError("Unsupported AFM release target")
    name = f"afm_{platform}_{arch}.tar.gz"
    manifest = json.loads((archives / "build-manifest.json").read_text())
    archive_path = archives / name
    with archive_path.open("rb") as archive_file:
        digest = hashlib.sha256(archive_file.read()).hexdigest()
        if digest != manifest["artifacts_sha256"].get(name):
            raise ValueError("Archive checksum mismatch: " + name)
        archive_file.seek(0)
        with tarfile.open(fileobj=archive_file, mode="r:gz") as archive:
            members = [member for member in archive.getmembers() if member.name == "afm"]
            if len(members) != 1 or not members[0].isfile():
                raise ValueError("Expected exactly one regular AFM executable in " + name)
            with archive.extractfile(members[0]) as binary:
                output.parent.mkdir(parents=True, exist_ok=True)
                output.write_bytes(binary.read())
                output.chmod(0o755)


if __name__ == "__main__":
    archives, platform, arch, output = sys.argv[1:]
    stage(pathlib.Path(archives), platform, arch, pathlib.Path(output))
