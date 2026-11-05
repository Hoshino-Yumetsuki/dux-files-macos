#!/bin/sh
set -eu

fail() { printf '%s\n' "error: $*" >&2; exit 1; }
[ "$(uname -s)" = Darwin ] || fail 'Package on macOS with the Apple SDK.'
: "${QT_STATIC_PREFIX:?Set QT_STATIC_PREFIX to the static Qt installation; shared/Homebrew Qt is rejected.}"
: "${QT_SOURCE_DIR:?Set QT_SOURCE_DIR to the matching verified Qt sources for license notices.}"
repo=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
build=${BUILD_DIR:-"$repo/build/macos-static"}
dist=${DIST_DIR:-"$repo/dist/macos-$(uname -m)"}
JOBS=${JOBS:-4}
QT_LICENSE=${QT_LICENSE:-opensource}
MACOSX_DEPLOYMENT_TARGET=${MACOSX_DEPLOYMENT_TARGET:-13.0}
case "$QT_LICENSE" in opensource|commercial) ;; *) fail 'QT_LICENSE must be opensource or commercial.' ;; esac
case "$JOBS" in ''|*[!0-9]*|0) fail 'JOBS must be a positive integer.' ;; esac
SIGNING_IDENTITY=${SIGNING_IDENTITY:-}
NOTARY_PROFILE=${NOTARY_PROFILE:-}
NOTARY_KEY=${NOTARY_KEY:-}
NOTARY_KEY_ID=${NOTARY_KEY_ID:-}
NOTARY_ISSUER=${NOTARY_ISSUER:-}
if [ -n "$SIGNING_IDENTITY$NOTARY_PROFILE$NOTARY_KEY$NOTARY_KEY_ID$NOTARY_ISSUER${SIGNING_KEYCHAIN:-}" ]; then
    [ -n "$SIGNING_IDENTITY" ] || fail 'Set SIGNING_IDENTITY to a Developer ID Application identity.'
    case "$SIGNING_IDENTITY" in 'Developer ID Application: '*) ;; *) fail 'SIGNING_IDENTITY must name a Developer ID Application identity.' ;; esac
    if [ -n "$NOTARY_PROFILE" ]; then
        [ -z "$NOTARY_KEY$NOTARY_KEY_ID$NOTARY_ISSUER" ] || fail 'Choose either NOTARY_PROFILE or the NOTARY_KEY credentials.'
    else
        [ -n "$NOTARY_KEY" ] && [ -f "$NOTARY_KEY" ] && [ -n "$NOTARY_KEY_ID" ] && [ -n "$NOTARY_ISSUER" ] \
            || fail 'Set NOTARY_PROFILE or all of NOTARY_KEY, NOTARY_KEY_ID and NOTARY_ISSUER.'
    fi
fi
for tool in cmake make go python3 otool ditto xcrun; do
    command -v "$tool" >/dev/null 2>&1 || fail "Missing builder tool: $tool"
done
xcrun --find xcodebuild >/dev/null 2>&1 && xcrun xcodebuild -version >/dev/null 2>&1 \
    || fail 'Full Xcode with a working xcodebuild is required; Command Line Tools alone are insufficient. Select an installed Xcode using DEVELOPER_DIR or xcode-select.'
xcrun --find clang++ >/dev/null 2>&1 || fail 'The selected Xcode has no clang++.'
xcrun --sdk macosx --show-sdk-path >/dev/null 2>&1 || fail 'The selected Xcode has no macOS SDK.'
python3 -I -c 'import sys; sys.exit(0 if sys.version_info >= (3, 10) else "Python 3.10 or newer is required.")'
QT_STATIC_PREFIX=$(CDPATH= cd -- "$QT_STATIC_PREFIX" && pwd)
QT_SOURCE_DIR=$(CDPATH= cd -- "$QT_SOURCE_DIR" && pwd)
[ -f "$QT_STATIC_PREFIX/lib/libQt6Core.a" ] || fail 'Missing lib/libQt6Core.a; build static Qt first.'
[ -d "$QT_SOURCE_DIR/qtbase/LICENSES" ] || fail 'Expected matching QT_SOURCE_DIR/qtbase/LICENSES.'
[ -f "$repo/quickshell/DCommon/Widgets/DIcon.qml" ] || fail 'Initialize the dux-qml-common submodule first.'
python3 -I - "$repo" "$QT_SOURCE_DIR" "$QT_STATIC_PREFIX" "$build" "$dist" <<'PY'
import pathlib
import sys
repo, source, prefix, build, dist = (pathlib.Path(value).resolve() for value in sys.argv[1:])
for output in (build, dist):
    if output == repo:
        raise SystemExit(f"Output must not be the repository root: {output}")
    for protected in (source, prefix, repo / "macos", repo / "core", repo / "quickshell", repo / "dux-qml-common"):
        if output.is_relative_to(protected) or protected.is_relative_to(output):
            raise SystemExit(f"Output overlaps source or Qt installation: {output}, {protected}")
if build.is_relative_to(dist) or dist.is_relative_to(build):
    raise SystemExit("BUILD_DIR and DIST_DIR must not overlap")
PY
mkdir -p "$build" "$dist"
build=$(CDPATH= cd -- "$build" && pwd)
dist=$(CDPATH= cd -- "$dist" && pwd)
[ ! -e "$dist/Dux Files.app" ] || fail 'DIST_DIR already contains Dux Files.app; select a fresh directory.'
[ ! -e "$dist/relink" ] || fail 'DIST_DIR already contains relink material; select a fresh directory.'

unset Qt6_DIR Qt6_ROOT QT_HOST_PATH QTDIR CMAKE_TOOLCHAIN_FILE
export MACOSX_DEPLOYMENT_TARGET
case "$(uname -m)" in
    arm64) backend_arch=arm64 ;;
    x86_64) backend_arch=amd64 ;;
    *) fail 'Unsupported native macOS architecture.' ;;
esac
GOOS=darwin GOARCH=$backend_arch CGO_ENABLED=0 make -C "$repo/core" build
cmake -S "$repo/macos" -B "$build" -G 'Unix Makefiles' \
    -DCMAKE_BUILD_TYPE=Release -DCMAKE_INTERPROCEDURAL_OPTIMIZATION=OFF \
    -DCMAKE_PREFIX_PATH="$QT_STATIC_PREFIX" \
    -DQt6_DIR="$QT_STATIC_PREFIX/lib/cmake/Qt6" \
    -DCMAKE_IGNORE_PREFIX_PATH='/opt/homebrew;/usr/local' \
    -DCMAKE_OSX_ARCHITECTURES="$(uname -m)" \
    -DCMAKE_OSX_DEPLOYMENT_TARGET="$MACOSX_DEPLOYMENT_TARGET" \
    -DDFILES_BACKEND="$repo/core/bin/dfiles" -DDFILES_REQUIRE_STATIC_QT=ON
cmake --build "$build" --target sync-bundle --parallel "$JOBS"
app="$build/Dux Files.app"
[ -d "$app/Contents/MacOS" ] || fail 'CMake must produce BUILD_DIR/Dux Files.app.'
[ -f "$app/Contents/Resources/qml/qs/shell.qml" ] || fail 'CMake must bundle qml/qs/shell.qml.'
python3 -I "$repo/macos/scripts/check-dependencies.py" "$app"

python3 -I - "$repo" "$QT_SOURCE_DIR" "$app" <<'PY'
import json
import pathlib
import shutil
import subprocess
import sys


def copy_notice(source, target):
    target.parent.mkdir(parents=True, exist_ok=True)
    if target.exists():
        target.unlink()
    shutil.copy2(source, target)


def attribution_license_files(value):
    if isinstance(value, dict):
        for key, nested in value.items():
            if key.lower() in ("licensefile", "licensefiles"):
                names = [nested] if isinstance(nested, str) else nested
                if not isinstance(names, list) or not names or not all(isinstance(name, str) and name for name in names):
                    raise ValueError(f"Invalid {key}: {nested!r}")
                yield from names
            else:
                yield from attribution_license_files(nested)
    elif isinstance(value, list):
        for nested in value:
            yield from attribution_license_files(nested)


def copy_qt_notices(root, target):
    root = root.resolve()
    copied = set()
    for path in root.rglob("*"):
        if not path.is_file():
            continue
        relative = path.relative_to(root)
        name = path.name.upper()
        if "LICENSES" in relative.parts or name.startswith(("LICENSE", "LICENCE", "COPYING", "COPYRIGHT", "NOTICE")) or path.name == "qt_attribution.json":
            copied.add(path)
    for attribution in root.rglob("qt_attribution.json"):
        try:
            entries = json.loads(attribution.read_text(encoding="utf-8"), strict=False)
            if not isinstance(entries, (dict, list)):
                raise ValueError("Expected attribution object or array")
            for license_file in attribution_license_files(entries):
                candidate = (attribution.parent / license_file).resolve()
                if not candidate.is_relative_to(root) or not candidate.is_file():
                    raise ValueError(f"Missing or non-module license file: {license_file}")
                copied.add(candidate)
        except (OSError, ValueError) as error:
            raise ValueError(f"{attribution}: {error}") from error
    for path in copied:
        copy_notice(path, target / path.relative_to(root))


repo, source, app = map(pathlib.Path, sys.argv[1:])
licenses = app / "Contents/Resources/licenses"
licenses.mkdir(parents=True, exist_ok=True)
copy_notice(repo / "LICENSE", licenses / "Dux-Files-MIT.txt")
copy_notice(repo / "dux-qml-common/LICENSE", licenses / "DCommon-MIT.txt")
for module in ("qtbase", "qtshadertools", "qtdeclarative", "qtsvg", "qt5compat", "qtimageformats"):
    root = source / module
    if not root.is_dir():
        raise SystemExit(f"Missing Qt source module for notices: {root}")
    copy_qt_notices(root, licenses / "Qt" / module)

gomod = subprocess.check_output(["go", "list", "-mod=readonly", "-m", "-json", "all"], cwd=repo / "core", text=True)
decoder = json.JSONDecoder()
while gomod.strip():
    module, end = decoder.raw_decode(gomod.lstrip())
    gomod = gomod.lstrip()[end:]
    directory = module.get("Replace", module).get("Dir")
    if not directory:
        continue
    root = pathlib.Path(directory)
    for path in root.rglob("*"):
        if not path.is_file() or not path.name.upper().startswith(("LICENSE", "LICENCE", "COPYING", "COPYRIGHT", "NOTICE")):
            continue
        target = licenses / "Go" / module["Path"] / path.relative_to(root)
        copy_notice(path, target)
goroot = pathlib.Path(subprocess.check_output(["go", "env", "GOROOT"], text=True).strip())
go_license = goroot / "LICENSE"
if not go_license.is_file():
    go_license = goroot.parent / "LICENSE"
copy_notice(go_license, licenses / "Go-LICENSE.txt")
fonts = app / "Contents/Resources/qml/qs/DCommon/assets/fonts"
if not list(fonts.rglob("*.ttf")):
    raise SystemExit(f"Missing original DCommon fonts: {fonts}")
for family in fonts.iterdir():
    if family.is_dir() and not any(path.name.upper().startswith(("LICENSE", "OFL", "COPYING")) for path in family.iterdir()):
        raise SystemExit(f"Missing font license: {family}")
PY

if [ "$QT_LICENSE" = opensource ]; then
    [ -f "$build/CMakeFiles/dfiles-ui.dir/link.txt" ] || fail 'Missing relink command from Unix Makefiles generator.'
    [ -f "$build/CMakeFiles/dfiles-native.dir/link.txt" ] || fail 'Missing native helper relink command.'
    mkdir -p "$dist/relink"
    python3 -I - "$build" "$dist/relink/build" <<'PY'
import pathlib
import shutil
import sys
source, target = map(pathlib.Path, sys.argv[1:])
shutil.copytree(source, target, ignore=shutil.ignore_patterns("*.app", "*.dSYM", "*.a", "*.dylib"))
PY
    cp "$repo/macos/scripts/build-static-qt.sh" "$dist/relink/build-static-qt.sh"
    cp "$repo/macos/README.md" "$dist/relink/README.md"
    cp -R "$repo/macos/patches" "$dist/relink/patches"
    printf '%s\n' \
        "Original host build directory: $build" \
        "Original Qt static prefix: $QT_STATIC_PREFIX" \
        "Qt sources: $QT_SOURCE_DIR" \
        'All application object files and generated static-plugin/resource objects are in build/.' \
        'Use build/CMakeFiles/dfiles-ui.dir/link.txt and build/CMakeFiles/dfiles-native.dir/link.txt; see README.md for relink instructions.' \
        'Provide the corresponding Qt/library source and any modifications when distributing under LGPL.' \
        > "$dist/relink/BUILD-PATHS.txt"
fi

ditto "$app" "$dist/Dux Files.app"
python3 -I "$repo/macos/scripts/check-dependencies.py" "$dist/Dux Files.app"
if [ -n "$SIGNING_IDENTITY" ]; then
    set --
    if [ -n "${SIGNING_KEYCHAIN:-}" ]; then
        set -- --keychain "$SIGNING_KEYCHAIN"
    fi
    app="$dist/Dux Files.app"
    codesign --force --timestamp --options runtime --sign "$SIGNING_IDENTITY" "$@" "$app/Contents/MacOS/dfiles"
    codesign --force --timestamp --options runtime --sign "$SIGNING_IDENTITY" "$@" "$app/Contents/MacOS/dfiles-native"
    codesign --force --timestamp --options runtime --entitlements "$repo/macos/entitlements.plist" --sign "$SIGNING_IDENTITY" "$@" "$app"
    codesign --verify --deep --strict --verbose=2 "$app"
    temp=$(mktemp -d "${TMPDIR:-/tmp}/dux-notary.XXXXXXXX")
    trap 'rm -rf "$temp"' EXIT HUP INT TERM
    ditto -c -k --keepParent "$app" "$temp/submission.zip"
    if [ -n "$NOTARY_PROFILE" ]; then
        xcrun notarytool submit "$temp/submission.zip" --keychain-profile "$NOTARY_PROFILE" --wait --output-format json > "$temp/result.json"
    else
        xcrun notarytool submit "$temp/submission.zip" --key "$NOTARY_KEY" --key-id "$NOTARY_KEY_ID" --issuer "$NOTARY_ISSUER" --wait --output-format json > "$temp/result.json"
    fi
    python3 -I - "$temp/result.json" <<'PY'
import json
import sys
with open(sys.argv[1], encoding="utf-8") as stream:
    result = json.load(stream)
print("Notarization submission:", result.get("id"), "status:", result.get("status"))
if result.get("status") != "Accepted":
    raise SystemExit("Notarization was not accepted; inspect the submission log with xcrun notarytool log.")
PY
    xcrun stapler staple "$app"
    xcrun stapler validate "$app"
    codesign --verify --deep --strict --verbose=2 "$app"
    ditto -c -k --keepParent "$app" "$dist/Dux Files.zip"
    printf '%s\n' "Notarized archive: $dist/Dux Files.zip"
fi
printf '%s\n' "Static application: $dist/Dux Files.app" "Qt license mode: $QT_LICENSE; review macos/README.md before distribution."
