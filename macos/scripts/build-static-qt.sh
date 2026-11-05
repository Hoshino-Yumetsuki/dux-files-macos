#!/bin/sh
set -eu

fail() { printf '%s\n' "error: $*" >&2; exit 1; }
[ "$(uname -s)" = Darwin ] || fail 'Static Qt must be built on macOS.'
: "${QT_SOURCE_DIR:?Set QT_SOURCE_DIR to a verified Qt source tree; see macos/README.md.}"
: "${QT_STATIC_PREFIX:?Set QT_STATIC_PREFIX to a dedicated static Qt installation directory.}"

repo=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
QT_VERSION=${QT_VERSION:-6.10.0}
QT_LICENSE=${QT_LICENSE:-opensource}
QT_BUILD_DIR=${QT_BUILD_DIR:-"$repo/build/qt-static-$QT_VERSION-$(uname -m)"}
JOBS=${JOBS:-4}
MACOSX_DEPLOYMENT_TARGET=${MACOSX_DEPLOYMENT_TARGET:-13.0}
case "$QT_LICENSE" in opensource|commercial) ;; *) fail 'QT_LICENSE must be opensource or commercial.' ;; esac
case "$JOBS" in ''|*[!0-9]*|0) fail 'JOBS must be a positive integer.' ;; esac
for tool in cmake ninja python3 xcrun; do
    command -v "$tool" >/dev/null 2>&1 || fail "Missing builder tool: $tool"
done
xcrun --find xcodebuild >/dev/null 2>&1 && xcrun xcodebuild -version >/dev/null 2>&1 \
    || fail 'Full Xcode with a working xcodebuild is required; Command Line Tools alone are insufficient. Select an installed Xcode using DEVELOPER_DIR or xcode-select.'
xcrun --find clang++ >/dev/null 2>&1 || fail 'The selected Xcode has no clang++.'
xcrun --sdk macosx --show-sdk-path >/dev/null 2>&1 || fail 'The selected Xcode has no macOS SDK.'
python3 -I -c 'import sys; sys.exit(0 if sys.version_info >= (3, 10) else "Python 3.10 or newer is required.")'
QT_SOURCE_DIR=$(CDPATH= cd -- "$QT_SOURCE_DIR" && pwd)
python3 -I -c '
import pathlib, sys
paths = [pathlib.Path(value).resolve() for value in sys.argv[1:]]
for index, left in enumerate(paths):
    for right in paths[index + 1:]:
        if left.is_relative_to(right) or right.is_relative_to(left):
            raise SystemExit(f"Source, build, and install directories must not overlap: {left}, {right}")
' "$QT_SOURCE_DIR" "$QT_BUILD_DIR" "$QT_STATIC_PREFIX"
[ ! -e "$QT_STATIC_PREFIX/lib/QtCore.framework" ] || fail 'Use a separate prefix, not a developer/shared Qt install.'
[ ! -e "$QT_STATIC_PREFIX/lib/libQt6Core.dylib" ] || fail 'Use a separate prefix, not a shared Qt install.'
[ -x "$QT_SOURCE_DIR/qtbase/configure" ] || fail 'Expected QT_SOURCE_DIR/qtbase/configure.'

for module in qtbase qtshadertools qtdeclarative qtsvg qt5compat qtimageformats; do
    [ -f "$QT_SOURCE_DIR/$module/CMakeLists.txt" ] || fail "Missing source module: $module"
    python3 -I -c '
import pathlib, re, sys
path = pathlib.Path(sys.argv[1]) / ".cmake.conf"
match = re.search(r"set\(QT_REPO_MODULE_VERSION\s+\"([^\"]+)\"", path.read_text())
if match is None or match.group(1) != sys.argv[2]:
    raise SystemExit(f"{path}: expected Qt {sys.argv[2]}; do not mix module versions")
' "$QT_SOURCE_DIR/$module" "$QT_VERSION"
done

# Do not let a developer/Homebrew Qt leak into this build.
mkdir -p "$QT_STATIC_PREFIX" "$QT_BUILD_DIR"
QT_STATIC_PREFIX=$(CDPATH= cd -- "$QT_STATIC_PREFIX" && pwd)
QT_BUILD_DIR=$(CDPATH= cd -- "$QT_BUILD_DIR" && pwd)
unset Qt6_DIR Qt6_ROOT QT_HOST_PATH QTDIR CMAKE_TOOLCHAIN_FILE
CMAKE_PREFIX_PATH=$QT_STATIC_PREFIX
PKG_CONFIG_PATH=
PKG_CONFIG_LIBDIR=/nonexistent
export CMAKE_PREFIX_PATH PKG_CONFIG_PATH PKG_CONFIG_LIBDIR MACOSX_DEPLOYMENT_TARGET

mkdir -p "$QT_BUILD_DIR/qtbase"
(
    cd "$QT_BUILD_DIR/qtbase"
    "$QT_SOURCE_DIR/qtbase/configure" \
        -prefix "$QT_STATIC_PREFIX" -static -release -no-framework \
        "-$QT_LICENSE" -confirm-license -nomake examples -nomake tests \
        -qt-zlib -qt-pcre -qt-libpng -qt-libjpeg -qt-freetype -qt-harfbuzz \
        -qt-doubleconversion -force-bundled-libs -no-pkg-config -no-dbus -no-icu -no-glib \
        -no-openssl -securetransport -no-feature-brotli -no-feature-zstd \
        -- -DCMAKE_OSX_DEPLOYMENT_TARGET="$MACOSX_DEPLOYMENT_TARGET" \
        -DCMAKE_OSX_ARCHITECTURES="$(uname -m)" \
        -DCMAKE_IGNORE_PREFIX_PATH='/opt/homebrew;/usr/local' \
        -DCMAKE_INTERPROCEDURAL_OPTIMIZATION=OFF \
        -DQT_BUILD_TESTS=OFF -DQT_BUILD_EXAMPLES=OFF
)
cmake --build "$QT_BUILD_DIR/qtbase" --parallel "$JOBS"
cmake --install "$QT_BUILD_DIR/qtbase"
[ -f "$QT_STATIC_PREFIX/lib/libQt6Core.a" ] || fail 'QtBase did not install a static libQt6Core.a.'

for module in qtshadertools qtdeclarative qtsvg qt5compat qtimageformats; do
    mkdir -p "$QT_BUILD_DIR/$module"
    (
        cd "$QT_BUILD_DIR/$module"
        set --
        if [ "$module" = qtimageformats ]; then
            set -- -qt-tiff -qt-webp -no-feature-jasper
        fi
        "$QT_STATIC_PREFIX/bin/qt-configure-module" "$QT_SOURCE_DIR/$module" "$@" \
            -- -DCMAKE_BUILD_TYPE=Release -DBUILD_SHARED_LIBS=OFF \
            -DCMAKE_OSX_DEPLOYMENT_TARGET="$MACOSX_DEPLOYMENT_TARGET" \
            -DCMAKE_OSX_ARCHITECTURES="$(uname -m)" \
            -DCMAKE_IGNORE_PREFIX_PATH='/opt/homebrew;/usr/local' \
            -DCMAKE_INTERPROCEDURAL_OPTIMIZATION=OFF \
            -DQT_BUILD_TESTS=OFF -DQT_BUILD_EXAMPLES=OFF
    )
    cmake --build "$QT_BUILD_DIR/$module" --parallel "$JOBS"
    cmake --install "$QT_BUILD_DIR/$module"
done
printf 'Qt %s (%s), source=%s, build=%s\n' "$QT_VERSION" "$QT_LICENSE" "$QT_SOURCE_DIR" "$QT_BUILD_DIR" > "$QT_STATIC_PREFIX/dux-static-build.txt"
printf 'Static Qt installed at %s\n' "$QT_STATIC_PREFIX"
