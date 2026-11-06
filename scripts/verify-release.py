#!/usr/bin/env python3
"""校验发布包的完整性：路径分隔符、可执行位、关键文件、内嵌后台资源。

Python 的 extractall 不还原权限，所以这里手动按 external_attr 恢复，
否则会误判"zip 里没有执行位"。
"""
import os
import shutil
import stat
import subprocess
import sys
import tempfile
import zipfile

NAME = "czapi-gw-v1.0.0-v10.1-linux-amd64"
ZIP = f"/mnt/d/code/CZAPI/{NAME}.zip"
TAR = f"/mnt/d/code/CZAPI/{NAME}.tar.gz"

passed = failed = 0


def check(name, ok, detail=""):
    global passed, failed
    if ok:
        passed += 1
        print(f"  PASS  {name}")
    else:
        failed += 1
        print(f"  FAIL  {name} {detail}")


def extract_zip(dest):
    with zipfile.ZipFile(ZIP) as zf:
        for info in zf.infolist():
            zf.extract(info, dest)
            mode = (info.external_attr >> 16) & 0o777
            if mode:
                os.chmod(os.path.join(dest, info.filename), mode)


print("== zip ==")
tmp = tempfile.mkdtemp()
try:
    if os.path.exists(tmp):
        shutil.rmtree(tmp, ignore_errors=True)
    os.makedirs(tmp, exist_ok=True)
    extract_zip(tmp)

    names = []
    for root, _dirs, files in os.walk(tmp):
        for f in files:
            names.append(os.path.relpath(os.path.join(root, f), tmp).replace(os.sep, "/"))

    check("无反斜杠路径", all("\\" not in n for n in names),
          [n for n in names if "\\" in n][:3])
    check("含安装脚本", "install.sh" in names)
    check("含 Linux 二进制", "bin/czapi-gw" in names)
    check("含后台脚本", any(n.startswith("scripts/v10-admin") for n in names))

    exe = os.path.join(tmp, "install.sh")
    check("install.sh 可执行", os.access(exe, os.X_OK),
          oct(os.stat(exe).st_mode) if os.path.exists(exe) else "missing")

    binary = os.path.join(tmp, "bin/czapi-gw")
    check("czapi-gw 可执行", os.access(binary, os.X_OK),
          oct(os.stat(binary).st_mode) if os.path.exists(binary) else "missing")
    if os.path.exists(binary):
        with open(binary, "rb") as fh:
            magic = fh.read(4)
        check("是 ELF 可执行文件", magic == b"\x7fELF", magic)
        size_mb = os.path.getsize(binary) / (1024 * 1024)
        check("二进制体积合理(>100MB, 含前端)", size_mb > 100, f"{size_mb:.1f}MB")

    # 后台资源是否真的被嵌进二进制：搜 CSS 里的特征串
    with open(binary, "rb") as fh:
        blob = fh.read()
    check("二进制含 /admin/ 前端资源", b"cz-sidebar" in blob or b"cz-login-card" in blob)
    check("二进制含 admin 路由字符串", b"/admin/" in blob)
finally:
    shutil.rmtree(tmp, ignore_errors=True)

print("== tar.gz ==")
tmp2 = tempfile.mkdtemp()
try:
    subprocess.run(["tar", "xzf", TAR, "-C", tmp2], check=True)
    names = []
    for root, _dirs, files in os.walk(tmp2):
        for f in files:
            names.append(os.path.relpath(os.path.join(root, f), tmp2).replace(os.sep, "/"))
    check("含安装脚本", "install.sh" in names)
    check("含 Linux 二进制", "bin/czapi-gw" in names)
    check("install.sh 可执行", os.access(os.path.join(tmp2, "install.sh"), os.X_OK))
    check("czapi-gw 可执行", os.access(os.path.join(tmp2, "bin/czapi-gw"), os.X_OK))
finally:
    shutil.rmtree(tmp2, ignore_errors=True)

print("----------------------------------------")
print(f"passed: {passed}   failed: {failed}")
sys.exit(0 if failed == 0 else 1)
