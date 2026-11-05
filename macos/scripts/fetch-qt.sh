#!/bin/sh
set -eu

: "${QT_SOURCE_DIR:?Set QT_SOURCE_DIR to a new Qt source directory.}"
: "${QT_ARCHIVE_DIR:?Set QT_ARCHIVE_DIR to a directory for the verified source archives.}"
QT_VERSION=${QT_VERSION:-6.10.0}
[ "$QT_VERSION" = 6.10.0 ] || { printf '%s\n' 'Only Qt 6.10.0 source hashes are pinned.' >&2; exit 1; }
[ ! -e "$QT_SOURCE_DIR" ] || { printf '%s\n' 'QT_SOURCE_DIR must not already exist.' >&2; exit 1; }
mkdir -p "$QT_ARCHIVE_DIR" "$QT_SOURCE_DIR"
while read -r module expected; do
    archive="$QT_ARCHIVE_DIR/$module-everywhere-src-$QT_VERSION.tar.xz"
    curl --fail --location --silent --show-error \
        "https://download.qt.io/archive/qt/6.10/$QT_VERSION/submodules/$module-everywhere-src-$QT_VERSION.tar.xz" \
        --output "$archive"
    printf '%s  %s\n' "$expected" "$archive" | shasum -a 256 --check
    mkdir "$QT_SOURCE_DIR/$module"
    tar -xJf "$archive" --strip-components=1 -C "$QT_SOURCE_DIR/$module"
done <<'HASHES'
qtbase ead4623bcb54a32257c5b3e3a5aec6d16ec96f4cda58d2e003f5a0c16f72046d
qtshadertools 87ba478c3fd827862fc79f6d78d369dfec162c901b7f66ed988b3e1d6ffdfbf6
qtdeclarative 6efd35520902395d865bc12e89f8442c3c228d0374f13af9a1888b844f56f6b0
qtsvg 5ed2c0e04d5e73ff75c2a2ed92db5dc1788ba70f704fc2b71bc21644beda2533
qt5compat d025c59d658b5e1f5fa1650bf01f23ddec15ce8f4c6ae5f6bc343f8427b02b7e
qtimageformats 64450a52507c540de53616ed5e516df0e0905a99d3035ddfaa690f2b3f7c0cea
HASHES
repo=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
patch -p1 -d "$QT_SOURCE_DIR/qtbase" -i "$repo/macos/patches/qtbase-6.10.0-arm-acle.patch"
patch -p1 -d "$QT_SOURCE_DIR/qtbase" -i "$repo/macos/patches/qtbase-6.10.0-cocoa-keymap.patch"
