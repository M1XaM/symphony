#!/usr/bin/env python3
"""Zips `make build` output into the release archives.

    package-release.py <version> <bin dir> <dist dir>

bin/linux/ becomes symphony-<version>-linux-amd64.zip (the window and the
CLI) and bin/windows/ becomes symphony-<version>-windows-amd64.zip (the two
.exe files and README.txt). Each archive holds one folder of the same name,
and SHA256SUMS lists both. File modes are kept, so the Linux binaries stay
executable when unzipped.
"""
import hashlib
import os
import sys
import zipfile

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

# platform directory -> archive suffix and files: (source, name in the archive)
ARCHIVES = {
    "linux": ("linux-amd64", [("symphony", None), ("symphony-cli", None)]),
    "windows": ("windows-amd64", [
        ("symphony.exe", None),
        ("symphony-cli.exe", None),
        (os.path.join(REPO, "src", "build", "windows", "README.txt"), "README.txt"),
    ]),
}


def main():
    if len(sys.argv) != 4:
        sys.exit(__doc__)
    version, bin_dir, dist = sys.argv[1:]
    os.makedirs(dist, exist_ok=True)
    sums = []
    for platform, (suffix, files) in ARCHIVES.items():
        name = f"symphony-{version}-{suffix}"
        archive = os.path.join(dist, name + ".zip")
        with zipfile.ZipFile(archive, "w", zipfile.ZIP_DEFLATED) as z:
            for src, arcname in files:
                path = src if os.path.isabs(src) else os.path.join(bin_dir, platform, src)
                if not os.path.isfile(path):
                    sys.exit(f"missing {path}: run `make build` first")
                info = zipfile.ZipInfo.from_file(path, f"{name}/{arcname or os.path.basename(src)}")
                info.compress_type = zipfile.ZIP_DEFLATED
                info.create_system = 3  # unix, so the mode below is honoured
                info.external_attr = (0o100755 if os.access(path, os.X_OK) else 0o100644) << 16
                with open(path, "rb") as f:
                    z.writestr(info, f.read())
        with open(archive, "rb") as f:
            sums.append(f"{hashlib.sha256(f.read()).hexdigest()}  {os.path.basename(archive)}\n")
        print(archive)
    with open(os.path.join(dist, "SHA256SUMS"), "w") as f:
        f.writelines(sums)


if __name__ == "__main__":
    main()
