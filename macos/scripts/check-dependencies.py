#!/usr/bin/env python3
"""Reject non-Apple dynamic dependencies from a macOS app bundle."""

from __future__ import annotations

import argparse
import os
import plistlib
import re
import subprocess
import sys
from dataclasses import dataclass
from pathlib import Path


@dataclass(frozen=True)
class Dependency:
    path: str
    classification: str


SYSTEM_PREFIXES = (
    "/System/Library/",
    "/usr/lib/",
    "/usr/lib/swift/",
    "/System/Volumes/Preboot/Cryptexes/OS/usr/lib/",
)


def classify_dependency(path: str) -> str:
    """Return ``system`` only for paths shipped by macOS itself."""
    if path.startswith("/"):
        path = os.path.normpath(path)
    if path.startswith(SYSTEM_PREFIXES):
        return "system"
    if path.startswith("@"):
        return "loader-relative"
    if path.startswith("/opt/homebrew/") or path.startswith("/usr/local/"):
        return "package-manager"
    if "/build/" in path or "/builds/" in path or "/CMakeFiles/" in path:
        return "build-tree"
    if path.startswith("/"):
        return "non-system"
    return "invalid"


def parse_otool(output: str) -> list[Dependency]:
    """Parse ``otool -L`` output without treating its file header as a dep."""
    dependencies: list[Dependency] = []
    headers = 0
    for line in output.splitlines():
        if not line.strip():
            continue
        if not line[0].isspace() and line.endswith(":"):
            headers += 1
            continue
        match = re.match(r"^\s+(.+?)\s+\(compatibility version .+\)$", line)
        if not match or not headers:
            raise ValueError(f"Unrecognized otool -L line: {line!r}")
        path = match.group(1)
        dependencies.append(Dependency(path, classify_dependency(path)))
    if not headers:
        raise ValueError("otool -L returned no Mach-O header")
    return dependencies


def is_macho(path: Path) -> bool:
    with path.open("rb") as stream:
        return stream.read(4) in {
            bytes.fromhex(magic)
            for magic in ("feedface", "cefaedfe", "feedfacf", "cffaedfe", "cafebabe", "bebafeca", "cafebabf", "bfbafeca")
        }


def macho_files(bundle: Path) -> list[Path]:
    return [path for path in bundle.rglob("*") if path.is_file() and not path.is_symlink() and is_macho(path)]


def inspect_file(path: Path) -> list[Dependency]:
    result = subprocess.run(
        ["otool", "-arch", "all", "-L", str(path)],
        check=False,
        capture_output=True,
        text=True,
    )
    if result.returncode:
        raise RuntimeError(f"otool -L failed for {path}: {result.stderr.strip()}")
    return parse_otool(result.stdout)


def inspect_types(path: Path) -> set[str]:
    result = subprocess.run(
        ["otool", "-arch", "all", "-hv", str(path)],
        check=False,
        capture_output=True,
        text=True,
    )
    if result.returncode:
        raise RuntimeError(f"otool -hv failed for {path}: {result.stderr.strip()}")
    types: set[str] = set()
    for line in result.stdout.splitlines():
        fields = line.split()
        if fields and fields[0] in ("MH_MAGIC", "MH_CIGAM", "MH_MAGIC_64", "MH_CIGAM_64"):
            if len(fields) < 5:
                raise ValueError(f"Unrecognized Mach-O header: {line!r}")
            types.add(fields[4])
    if not types:
        raise ValueError(f"otool -hv returned no Mach-O type: {path}")
    return types


def check_bundle(bundle: Path) -> list[str]:
    failures: list[str] = []
    if not bundle.is_dir():
        return [f"bundle does not exist: {bundle}"]
    root = bundle.resolve()
    for path in bundle.rglob("*"):
        if path.is_symlink() and not path.resolve().is_relative_to(root):
            failures.append(f"{path}: symlink escapes the app bundle")
    with (bundle / "Contents/Info.plist").open("rb") as stream:
        executable = plistlib.load(stream).get("CFBundleExecutable", "")
    if not isinstance(executable, str) or not executable or Path(executable).name != executable:
        return [*failures, "Info.plist must name a bundle-local CFBundleExecutable"]
    required = {bundle / "Contents/MacOS" / name for name in (executable, "dfiles", "dfiles-native")}
    for path in sorted(required):
        if not path.is_file() or not is_macho(path):
            failures.append(f"{path}: missing Mach-O executable")
    files = macho_files(bundle)
    if not files:
        return [*failures, f"no Mach-O files found in bundle: {bundle}"]
    for binary in files:
        types = inspect_types(binary)
        if types != {"EXECUTE"} or re.search(r"\.(dylib|so)(\.|$)|\.framework/", str(binary.relative_to(bundle))):
            failures.append(f"{binary}: bundled dynamic library or non-executable Mach-O ({', '.join(sorted(types))}); link its static archive instead")
        for dependency in inspect_file(binary):
            if dependency.classification != "system":
                failures.append(f"{binary}: {dependency.path} ({dependency.classification})")
    return failures


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("bundle", type=Path)
    args = parser.parse_args()
    try:
        failures = check_bundle(args.bundle.resolve())
    except (OSError, RuntimeError, ValueError, plistlib.InvalidFileException) as error:
        print(f"dependency check failed: {error}", file=sys.stderr)
        return 2
    if failures:
        print("Non-system dynamic dependencies found:", file=sys.stderr)
        print("\n".join(f"- {failure}" for failure in failures), file=sys.stderr)
        return 1
    print(f"Dependency check passed: {args.bundle} (Apple system dependencies only)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
