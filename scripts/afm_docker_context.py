#!/usr/bin/env python3
"""Prepare a source-free Docker context from verified AFM release archives."""
import hashlib
import json
import pathlib
import re
import shutil
import subprocess
import sys
import tarfile


def image_tags(tag):
    match = re.fullmatch(r"v([1-9][0-9]*)\.[0-9]+\.[0-9]+(?:-(?:alpha|beta)\.[0-9]+)?", tag)
    if not match:
        raise ValueError("Expected a stable, alpha or beta release tag")
    tags = ["akopichin/afm:" + tag]
    if "-" not in tag:
        tags.append("akopichin/afm:" + match[1])
        if int(match[1]) >= 3:
            tags.append("akopichin/afm:latest")
    return tags


def prepare(product, archives, destination, tag):
    tags = image_tags(tag)
    manifest = json.loads((archives / "build-manifest.json").read_text())
    commit = subprocess.check_output(
        ["git", "-C", str(product), "rev-parse", "HEAD"], text=True
    ).strip()
    if manifest["afm_commit"] != commit:
        raise ValueError("Archives do not belong to the checked-out AFM commit")
    expected = {
        "afm_" + platform + "_" + arch + suffix
        for platform, suffix in (("linux", ".tar.gz"), ("darwin", ".tar.gz"), ("windows", ".zip"))
        for arch in ("amd64", "arm64")
    }
    hashes = manifest["artifacts_sha256"]
    if set(hashes) != expected:
        raise ValueError("Expected all six release archives")
    for name, digest in hashes.items():
        if hashlib.sha256((archives / name).read_bytes()).hexdigest() != digest:
            raise ValueError("Archive checksum mismatch: " + name)

    destination.mkdir(parents=True, exist_ok=False)
    sources = {
        product / "Dockerfile.runtime": "Dockerfile.runtime",
        product / "docker-entrypoint.sh": "docker-entrypoint.sh",
        product / "scripts/telegram-notify.sh": "scripts/telegram-notify.sh",
        product / "contracts/adapters/install-cursor-agent.sh": "scripts/install-cursor-agent.sh",
    }
    for source in (product / "contracts/adapters").glob("*-as-claude.sh"):
        sources[source] = "scripts/" + source.name
    for source, relative in sources.items():
        if source.is_symlink() or not source.is_file():
            raise ValueError("Docker context requires a regular public file: " + str(source))
        target = destination / relative
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(source, target)
    for arch in ("amd64", "arm64"):
        with tarfile.open(archives / ("afm_linux_" + arch + ".tar.gz")) as archive:
            member = archive.getmember("afm")
            if not member.isfile():
                raise ValueError("Release binary must be a regular file")
            target = destination / "binaries" / ("linux_" + arch) / "afm"
            target.parent.mkdir(parents=True)
            with archive.extractfile(member) as source:
                target.write_bytes(source.read())
            target.chmod(0o755)
    return tags


if __name__ == "__main__":
    product, archives, destination = map(pathlib.Path, sys.argv[1:4])
    print("tags=" + ",".join(prepare(product, archives, destination, sys.argv[4])))
