#!/usr/bin/env python3
"""先保存不可变资源，再逐文件验证浏览器实际访问的同源地址。"""
import argparse
import filecmp
import hashlib
import os
from pathlib import Path
import re
import subprocess
import tempfile
from urllib.request import urlopen

root = Path(os.environ.get("OMNI_UI_ARCHIVE", "/var/lib/omnirelay/ui"))
manifest = Path(__file__).resolve().parents[1] / "base/go/nginx/legacy-files.txt"


def store(source, target):
    target.parent.mkdir(parents=True, exist_ok=True, mode=0o755)
    source.chmod(0o644)
    try:
        os.link(source, target)
    except FileExistsError:
        if not filecmp.cmp(source, target, shallow=False):
            raise ValueError(f"同一路径对应不同内容：{target.name}")


def download(origin, path, archived=False):
    with urlopen(origin.rstrip("/") + "/" + path, timeout=20) as response:
        if response.status != 200 or response.headers.get_content_type() == "text/html":
            raise ValueError(f"资源响应无效：{path}")
        if archived and response.headers.get("X-OmniRelay-Asset-Archive") != "1":
            raise ValueError(f"同源请求未命中静态归档：{path}")
        return response.read()


def publish_image(image, origin, stage):
    if not re.fullmatch(r"[\w./:-]+@sha256:[a-f0-9]{64}", image):
        raise ValueError("需要镜像仓库地址及 SHA256 摘要")
    subprocess.run(["docker", "pull", image], check=True)
    container = subprocess.check_output(["docker", "create", image], text=True).strip()
    try:
        subprocess.run(["docker", "cp", container + ":/opt/omnirelay/ui/assets", str(stage / "assets")], check=True)
    finally:
        subprocess.run(["docker", "rm", container], check=True)
    files = sorted((stage / "assets").iterdir())
    if not files:
        raise ValueError("镜像缺少静态资源")
    for source in files:
        if source.is_symlink() or not source.is_file() or not re.fullmatch(r"[\w.-]+-[\w-]{8}\.[\w]+", source.name):
            raise ValueError(f"无效的哈希资源：{source.name}")
        name = "assets/" + source.name
        store(source, root / name)
        if hashlib.sha256(download(origin, name, archived=True)).digest() != hashlib.sha256(source.read_bytes()).digest():
            raise ValueError(f"同源入口资源校验失败：{name}")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=["legacy", "verify-legacy", "image"])
    parser.add_argument("origin")
    parser.add_argument("image", nargs="?")
    args = parser.parse_args()
    os.umask(0o022)
    root.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(dir=root) as scratch:
        stage = Path(scratch)
        if args.action == "image":
            publish_image(args.image or "", args.origin, stage)
            return
        for name in manifest.read_text().splitlines():
            data = download(args.origin, name, archived=args.action == "verify-legacy")
            if args.action == "verify-legacy":
                if (root / "legacy" / name).read_bytes() != data:
                    raise ValueError(f"历史资源校验失败：{name}")
            else:
                source = stage / name
                source.write_bytes(data)
                store(source, root / "legacy" / name)


if __name__ == "__main__":
    main()
