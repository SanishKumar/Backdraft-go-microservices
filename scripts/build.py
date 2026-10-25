"""Build a portable Backdraft executable with its UI, using installed Go and Node."""

import argparse
import os
from pathlib import Path
import shutil
import subprocess


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--version", default="dev")
    parser.add_argument("--out", default="dist")
    args = parser.parse_args()
    if not args.version or not all(c.isascii() and (c.isalnum() or c in ".-_") for c in args.version):
        parser.error("version must contain ASCII letters, digits, dots, dashes or underscores")
    root = Path(__file__).resolve().parents[1]
    out = (root / args.out).resolve()
    out.mkdir(parents=True, exist_ok=True)
    npm = shutil.which("npm.cmd" if os.name == "nt" else "npm")
    go = shutil.which("go")
    if not npm or not go:
        parser.error("Go and Node.js/npm must be installed and on PATH")
    subprocess.run([npm, "--prefix", "web", "ci", "--no-audit", "--no-fund"], cwd=root, check=True)
    subprocess.run([npm, "--prefix", "web", "run", "build"], cwd=root, check=True)
    target = out / ("backdraft.exe" if os.name == "nt" else "backdraft")
    subprocess.run([go, "build", "-mod=vendor", "-tags", "webui", "-trimpath",
                    "-ldflags", f"-s -w -X main.version={args.version}", "-o", str(target), "./cmd/backdraft"], cwd=root, check=True)
    print(f"Built {target}\nRun: {target} demo")


if __name__ == "__main__":
    main()
