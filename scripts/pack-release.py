#!/usr/bin/env python3
"""把发布目录打成 zip 与 tar.gz。

Windows 的 Compress-Archive 会写 `\\` 分隔符、且丢掉可执行位，Linux 解开后
install.sh 跑不起来。这里用 Python 显式写 `/` 与 st_mode，保证解压即用。
"""
import os
import sys
import stat
import tarfile
import zipfile

SRC = "/mnt/d/code/CZAPI/.research/stage/czapi-gw-v1.0.0-v10.1"
OUT_DIR = "/mnt/d/code/CZAPI"
NAME = "czapi-gw-v1.0.0-v10.1-linux-amd64"

# 必须可执行的文件
EXEC_NAMES = {"install.sh", "czapi-gw"}


def iter_files(root):
    for dirpath, dirnames, filenames in os.walk(root):
        dirnames.sort()
        for name in sorted(filenames):
            full = os.path.join(dirpath, name)
            rel = os.path.relpath(full, root).replace(os.sep, "/")
            yield full, rel


def mode_for(rel):
    base = os.path.basename(rel)
    if base in EXEC_NAMES:
        return 0o100755
    return 0o100644


def build_zip():
    out = os.path.join(OUT_DIR, NAME + ".zip")
    with zipfile.ZipFile(out, "w", zipfile.ZIP_DEFLATED, compresslevel=9) as zf:
        for full, rel in iter_files(SRC):
            info = zipfile.ZipInfo(rel)
            info.create_system = 3  # Unix，zipfile 据此解释 external_attr
            info.external_attr = mode_for(rel) << 16
            info.compress_type = zipfile.ZIP_DEFLATED
            with open(full, "rb") as fh:
                zf.writestr(info, fh.read())
    return out


def build_tar():
    out = os.path.join(OUT_DIR, NAME + ".tar.gz")
    with tarfile.open(out, "w:gz") as tf:
        for full, rel in iter_files(SRC):
            info = tf.gettarinfo(full, arcname=rel)
            info.mode = 0o755 if os.path.basename(rel) in EXEC_NAMES else 0o644
            info.uid = info.gid = 0
            info.uname = info.gname = "root"
            with open(full, "rb") as fh:
                tf.addfile(info, fh)
    return out


if __name__ == "__main__":
    for path in (build_zip(), build_tar()):
        size = os.path.getsize(path) / (1024 * 1024)
        print(f"{os.path.basename(path)}  {size:.1f} MB")
