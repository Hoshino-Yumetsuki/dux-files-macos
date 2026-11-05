# macOS static release

The macOS Qt host uses the repository's original QML, Theme, DCommon widgets,
icons and fonts. It supplies a macOS platform bridge rather than a replacement
interface. Linux continues to use Quickshell.

A release contains `Dux Files.app`, with Qt and its selected plugins linked into
the UI executable, a CGO-free `dfiles` backend, and a `dfiles-native` Apple-platform
helper in `Contents/MacOS`. End users
should not need Qt, Homebrew, Go, Python or Quickshell installed. Apple frameworks
and `/usr/lib` remain dynamic: macOS does not support statically linking its
system libraries. Builder tools can come from Homebrew without becoming runtime
dependencies.

Finder and Dock use `assets/dev.duxlabs.files.icns`, generated from the existing
`assets/dev.duxlabs.files.svg`. To refresh it after changing the SVG, render an
iconset with `rsvg-convert` at 16, 32, 128, 256 and 512 points, each at 1x and 2x,
then convert it with `iconutil -c icns`. The generated ICNS is checked in; normal
builds copy it into `Contents/Resources` and require no image-conversion tools.

## Development and checks

The shared Qt development build runs the original interface on Cocoa. With the
repository dependencies prepared and Qt available to CMake:

```sh
make macos CMAKE_ARGS='-DCMAKE_PREFIX_PATH=/opt/homebrew -DQT_ADDITIONAL_PACKAGES_PREFIX_PATH=/opt/homebrew -DDFILES_BUILD_TESTS=ON'
ctest --test-dir builddir/macos-dev --output-on-failure
"builddir/macos-dev/Dux Files.app/Contents/MacOS/Dux Files"
```

Adjust the Qt prefix for your installation. `sync-bundle` is the default build's
bundle synchronization target; use it after explicitly building `dfiles-ui` to
copy the current backend and resources into the app. Rebuilds replace the staged
and bundled QML trees so removed assets do not survive. Changes to the original
QML tree, including DCommon, trigger CMake restaging.

CTest covers file/process/socket adapters, the offscreen file clipboard, native
NSPasteboard file/image data and cross-process cut ownership on an isolated named
pasteboard, native PDF/image rendering, macOS key bindings, the original QML
interaction fixtures, host startup/path handling/quit, staging and dependency/notice
checks. The Linux clipboard-IPC fixture is replaced by the native clipboard checks.
The device/network fixture waits for the dialog's window overlay before running
visibility checks on either host.
Set
`QT_SOURCE_DIR` when running CTest to also check the real Qt attribution files.
The clipboard check forces Qt's offscreen platform so it does not replace the
user's system clipboard.

For the same regression suite against static Qt, use a separate verification
build. This adds the offscreen and QtTest plugins only to the verification host;
the release host imports Cocoa only.

```sh
cmake -S macos -B build/macos-static-checks \
  -DCMAKE_BUILD_TYPE=Release -DCMAKE_PREFIX_PATH="$QT_STATIC_PREFIX" \
  -DDFILES_REQUIRE_STATIC_QT=ON -DDFILES_BUILD_TESTS=ON
cmake --build build/macos-static-checks --parallel 4
QT_SOURCE_DIR="$QT_SOURCE_DIR" ctest --test-dir build/macos-static-checks --output-on-failure
```

The full native window check can be run separately:
```sh
QT_QPA_PLATFORM=cocoa builddir/macos-dev/shell_test "$PWD/quickshell" /tmp/dux-rounded.png
```

Run the native window check in an unlocked desktop session. On November 1, 2026,
the unlocked desktop passed the full shell check, including native activation,
rounded rendering, maximize, minimize, restore and intercepted close. The nine
CTest groups do not include this Cocoa check. Separate native AppleEvent checks
verified Unicode file/folder delivery and reopening a hidden window through the
Dock reopen event; these drivers live in the ignored development build tree.
A later repeat rendered the original UI but failed the native-focus assertion
while `CGSSessionScreenIsLocked` was true; focus must be rechecked after unlocking.

The host accepts absolute or relative local paths and supported location URIs.
`--config` expects a staged `qs` directory with generated `qmldir` files, alongside
its import root. File copy/cut/paste uses native file URLs on macOS; an external
selection is copied, and clearing Dux's selection preserves newer clipboard data
from another application. Native file URLs, PNG/TIFF image reading and cross-process
ownership are covered with a named pasteboard; Finder interaction and the full UI
image-paste operation still need end-to-end validation.

The current development Qt libraries require newer macOS versions than the
recipe's macOS 13 target. Running this development build does not verify macOS 13
compatibility or a self-contained release.

## Full Disk Access

The first visible main window offers Full Disk Access guidance once per Dux
profile, including existing profiles that have not seen the guide. **Open System
Settings** requests the Privacy & Security → Full Disk Access pane. Enable Dux
Files there, or use **+** to add the copy of `Dux Files.app` you run, then quit and
reopen the app. **Not now**, Escape, or closing the guide lets browsing continue.

`fullDiskAccessOffered` in `ui-settings.json` records only that the guide was shown;
it is not an authorization status. Opening Settings never grants access. There is
no background permission polling, protected-file probe, or automatic TCC change.
The guide explains access to protected app data, backups and other users' data.
Filesystem permissions and ACLs still apply, and access errors remain visible.
New guide strings use `I18n.tr()` and fall back to English until POEditor sync.

## Builder preparation

Use macOS, **full Xcode with a working `xcrun xcodebuild -version`**, CMake
>= 3.22, Ninja, Make, Python 3.10+, and the Go version required by `core/go.mod`.
Command Line Tools alone are insufficient for this Qt source build. Both scripts
check `xcodebuild`, `clang++` and the macOS SDK before creating build directories.
Select an already installed Xcode with `DEVELOPER_DIR` or `xcode-select`; the scripts
do not change that selection. Qt 6.10.0's own `.cmake.conf` declares minimum Xcode 15
and macOS SDK 14; the default deployment target is macOS 13. The actual compiler and
SDK must still pass Qt's checks, without fabricated version overrides.

Prepare the repository's Go dependencies and
DCommon submodule before building. Scripts do not install tools or automatically
download Qt source archives. The normal Go build can use the Go toolchain's module
cache/network behavior; use a prepared cache and `GOPROXY=off GOTOOLCHAIN=local`
when you need to enforce an offline build.

Obtain and verify pinned, matching Qt sources yourself. `QT_SOURCE_DIR` must
contain `qtbase`, `qtshadertools`, `qtdeclarative`, `qtsvg`, `qt5compat` and
`qtimageformats`, including their source dependencies. All six modules must match
`QT_VERSION`; the recipe checks their `.cmake.conf`. Record source checksums or
commits, local patches, compiler/SDK and configure options with your release.
The source archive's origin is the builder's responsibility.

The script's default is **Qt 6.10.0**, and the commands below use that exact
release. The locally installed shared Qt 6.12 is a development build and cannot
produce a static release. Keep the static Qt prefix and build tree separate from
that installation and from the source tree. Use fresh directories for each
version/configuration. Source, Qt build and Qt prefix paths must not overlap.
The scripts build the builder's native architecture; create arm64 and x86_64
releases on the respective builders. There is no universal application assembly step.

The six official module archives total about 103 MiB; QtWebEngine and the full
multi-gigabyte Qt everywhere archive are unnecessary. The official base URL is
`https://download.qt.io/archive/qt/6.10/6.10.0/submodules/`, and each filename is
`<module>-everywhere-src-6.10.0.tar.xz`. These SHA-256 values were checked against
the corresponding official `.tar.xz.mirrorlist` files. Download over HTTPS and
verify with `shasum -a 256` before extracting; apply your release's signature
verification policy too. This hash table is not an independent digital signature.

| Module | SHA-256 |
| --- | --- |
| qtbase | `ead4623bcb54a32257c5b3e3a5aec6d16ec96f4cda58d2e003f5a0c16f72046d` |
| qtshadertools | `87ba478c3fd827862fc79f6d78d369dfec162c901b7f66ed988b3e1d6ffdfbf6` |
| qtdeclarative | `6efd35520902395d865bc12e89f8442c3c228d0374f13af9a1888b844f56f6b0` |
| qtsvg | `5ed2c0e04d5e73ff75c2a2ed92db5dc1788ba70f704fc2b71bc21644beda2533` |
| qt5compat | `d025c59d658b5e1f5fa1650bf01f23ddec15ce8f4c6ae5f6bc343f8427b02b7e` |
| qtimageformats | `64450a52507c540de53616ed5e516df0e0905a99d3035ddfaa690f2b3f7c0cea` |

Extract each verified archive into its own new empty directory, then move its
single source root to the module name shown below. Do not run an interpreter or
build command from an unverified download directory. A same-version supermodule
checkout containing the six complete modules also works. A top-level Qt configure
script is not required: the recipe installs QtBase first, then builds five separate
modules against that installation.

```text
QT_SOURCE_DIR/
  qtbase/configure
  qtbase/.cmake.conf
  qtshadertools/CMakeLists.txt
  qtdeclarative/CMakeLists.txt
  qtsvg/CMakeLists.txt
  qt5compat/CMakeLists.txt
  qtimageformats/CMakeLists.txt
```

For Qt 6.10.0, apply the retained ARM and Cocoa keyboard fixes before building.
`fetch-qt.sh` applies both automatically to its newly verified sources; do not
apply them twice. For manually prepared sources:

```sh
export QT_SOURCE_DIR="$PWD/builddir/qt-source-6.10.0"
patch -p1 -d "$QT_SOURCE_DIR/qtbase" < macos/patches/qtbase-6.10.0-arm-acle.patch
patch -p1 -d "$QT_SOURCE_DIR/qtbase" < macos/patches/qtbase-6.10.0-cocoa-keymap.patch
```

The patch includes `<arm_acle.h>` on ARM so `__yield()` is declared. This is the
failure documented in [QTBUG-145239](https://bugreports.qt.io/browse/QTBUG-145239)
and [Bitcoin's Clang 21 build report](https://github.com/bitcoin/bitcoin/issues/34951).
Qt's upstream fix instead prioritizes `__builtin_arm_yield()`; this local patch
keeps the pinned 6.10.0 intrinsic selection unchanged. Apply it once to the verified
sources, and retain the modified sources and patch with the release.

The Cocoa patch uses `NSEvent.charactersByApplyingModifiers` when Carbon returns
no character for a key. On the local macOS 27.2 Chinese input source, Qt 6.10.0
otherwise receives `Key_T` but builds the shortcut candidate `Ctrl+` rather than
`Ctrl+T`. Valid Carbon translations remain unchanged. Keep this patch with the
matching Qt sources and relink materials.

```sh
export QT_VERSION=6.10.0
export QT_SOURCE_DIR="$PWD/builddir/qt-source-$QT_VERSION"
export QT_STATIC_PREFIX="$PWD/builddir/qt-static-prefix-$QT_VERSION"
export QT_BUILD_DIR="$PWD/builddir/qt-static-build-$QT_VERSION-$(uname -m)"
export MACOSX_DEPLOYMENT_TARGET=13.0
export QT_LICENSE=opensource
sh macos/scripts/build-static-qt.sh
```

The recipe builds static, release, non-framework QtBase and then the five
additional modules with `qt-configure-module`. It forces bundled third-party
libraries where supported, including zlib, PCRE, JPEG/PNG, FreeType, HarfBuzz,
double-conversion, TIFF and WebP. OpenSSL, D-Bus, ICU, GLib, Brotli and zstd are
disabled; TLS uses Apple's native backend. Homebrew prefixes are excluded from
library discovery. Unknown configure flags fail the build: consult the pinned
source version's `configure -help` when changing Qt releases. `JOBS` overrides
parallelism. Keep the Qt source, build, prefix and configure logs for relinking.

## Packaging

Keep `QT_SOURCE_DIR` pointed at the exact sources corresponding to your static
Qt prefix, including modifications. Use distinct source, Qt prefix, host build
and distribution directories, with build/distribution trees outside application
source directories. `DIST_DIR` must not already contain `Dux Files.app` or
`relink/`.

```sh
export QT_VERSION=6.10.0
export QT_SOURCE_DIR="$PWD/builddir/qt-source-$QT_VERSION"
export QT_STATIC_PREFIX="$PWD/builddir/qt-static-prefix-$QT_VERSION"
export BUILD_DIR="$PWD/build/macos-static"
export DIST_DIR="$PWD/dist/macos-$(uname -m)"
export QT_LICENSE=opensource
sh macos/scripts/package.sh
```

Packaging runs the existing backend release build with `CGO_ENABLED=0`, then
configures the host with `DFILES_REQUIRE_STATIC_QT=ON` and builds `sync-bundle`,
which builds the `dfiles-ui` and `dfiles-native` targets before synchronizing the backend,
helper and resources. It stages `build/qml/qs` into `Contents/Resources/qml/qs`,
copies the backend to `Contents/MacOS/dfiles` and the helper to
`Contents/MacOS/dfiles-native`, checks dependencies and creates an `.app` distribution.
No GUI is launched. Without signing variables the platform toolchain applies only
its normal ad-hoc Mach-O signature. For Developer ID distribution, set
`SIGNING_IDENTITY` to a `Developer ID Application: ...` identity and either
`NOTARY_PROFILE` (a notarytool keychain profile), or all of `NOTARY_KEY` (a `.p8`
file), `NOTARY_KEY_ID` and `NOTARY_ISSUER`. `SIGNING_KEYCHAIN` optionally selects
the signing keychain. Packaging signs the nested executables before the app,
enables the hardened runtime with QML's JIT entitlement, submits a temporary ZIP,
requires an `Accepted` result, staples and validates the ticket, verifies the
signature, and creates `Dux Files.zip`. It never substitutes ad-hoc signing after
a requested Developer ID signing or notarization failure. Gatekeeper and
clean-machine execution remain separate release checks.

The macOS workflow's `sign` input enables this path using the secrets
`MACOS_CERTIFICATE_P12_BASE64`, `MACOS_CERTIFICATE_PASSWORD`, `MACOS_SIGNING_IDENTITY`,
`MACOS_NOTARY_KEY_BASE64`, `MACOS_NOTARY_KEY_ID` and `MACOS_NOTARY_ISSUER`.
The certificate and API-key secrets are base64-encoded. A temporary keychain is
removed, together with the private-key files, even when the job fails.

Every branch push runs the macOS arm64 workflow without signing secrets and
uploads `dfiles-macos-arm64` for 14 days. Its tarball contains the app and
`relink/`, including the verified Qt source archives, patches and build recipe;
the adjacent SHA-256 file checks the tarball. The static Qt cache is keyed by
toolchain, build inputs and absolute Qt paths, so a changed runner path rebuilds
Qt rather than reusing an installation with stale paths. Tag pushes use the same
workflow once through the release workflow, which attaches its artifacts to the
release. Set the repository variable `MACOS_SIGN_RELEASE=true` to request signing
for tags; a manually dispatched build can also enable `sign`. Signed builds add
the notarized ZIP and its checksum. An unsigned app is not notarized and may be
blocked by Gatekeeper.

The original QML asset tree includes its font files and OFL/LICENSE notices.
Packaging checks that bundled fonts and family license notices exist. Project
and DCommon MIT notices, Qt/third-party attribution files found in the supplied
sources, Go module notices and Go's license are collected under
`Contents/Resources/licenses`. Qt attribution `LicenseFile` and `LicenseFiles`
references are followed, including notices with nonstandard filenames; malformed
metadata, missing references and references outside the module fail packaging.
Review the actual Qt LICENSES directory and the
exact third-party notices required by your build before distribution; mechanical
copying is not a license audit or a guarantee that every required notice is found.

`check-dependencies.py` checks the executables identified by Info.plist and the
bundled `dfiles` and `dfiles-native`, then examines every embedded Mach-O, including all architectures
and nested helpers/plugins, using `otool -arch all -L`. It permits Apple system
library paths, rejects `@rpath`, `@loader_path`, `@executable_path`, Homebrew,
build-tree and other non-system dependencies, rejects external bundle symlinks,
and flags embedded loadable libraries by their Mach-O type, even if renamed.
Unexpected Mach-O types and malformed `otool` output also fail validation. Shared Qt is rejected rather than copied into the bundle;
`macdeployqt` is not used.

```sh
python3 -I macos/scripts/check-dependencies.py "dist/macos-$(uname -m)/Dux Files.app"
python3 -I -m unittest discover -s macos/tests -v
```

The checker examines linked Mach-O dependencies. It does not audit `LC_RPATH`,
arbitrary `dlopen()` strings or processes invoked at runtime, and cannot prove
that every QML view loads. It checks Mach-O file types via `otool -arch all -hv`,
so a renamed DYLIB/BUNDLE cannot pass just by depending only on system libraries.
Static imports and a clean-machine UI smoke test
are still necessary. Re-run validation after modifying/signing the final app.

## Qt licensing and relinking

Static linking does not remove Qt licensing obligations. LGPL-covered Qt
requires recipients to be able to modify Qt and relink the application, with
applicable corresponding source, notices, license texts and any required
installation information. Some modules have other terms. A suitable commercial
Qt license is another route; setting `QT_LICENSE=commercial` does not establish
commercial entitlement. Third-party and font licenses still apply. These scripts
are technical aids, not legal advice or a guarantee of compliance.

See the official Qt licensing material and review the exact terms for your
chosen versions and delivery model:

- [Qt licensing](https://doc.qt.io/qt-6/licensing.html)
- [Qt LGPL obligations](https://www.qt.io/development/open-source-lgpl-obligations)
- [LGPLv3 text](https://doc.qt.io/qt-6/lgpl.html)

With `QT_LICENSE=opensource`, packaging adds `relink/` next to the app:

- `build/`: host object files, generated static-plugin/resource code and CMake
  build metadata, excluding app/dSYM bundles and library archives/dylibs.
- `build/CMakeFiles/dfiles-ui.dir/link.txt` and
  `build/CMakeFiles/dfiles-native.dir/link.txt`: compiler/linker commands; retain
  any referenced response files from the same build tree.
- `BUILD-PATHS.txt`: the original host build, Qt prefix and Qt source paths.
- `build-static-qt.sh`, `patches/` and this `README.md`: recipe, Qt source patches
  and relink instructions.

Application objects are retained and CMake IPO is disabled to support relinking.
**The script does not package corresponding Qt/library source or a source offer.**
Before distributing an LGPL build, provide the exact corresponding Qt and bundled
library sources, patches, configure records and any additional required materials
along with these objects/instructions, using a distribution method permitted by
the applicable licenses. Keep the matching MIT-licensed application and DCommon
sources available too. Distribute the relink materials alongside the app; copying
only the `.app` omits the object files. Verify the source/licensing contents of the
actual release, including all Qt license texts under each module's `LICENSES/`.

To relink with modified Qt, build it with the same Qt version, ABI/configuration,
architecture and SDK. Copy `relink/build` to a working directory. Read
`BUILD-PATHS.txt` and both target `link.txt` files. From that working
directory, run the recorded compiler/link commands with old host/source paths
updated to the extracted matching sources/build and old Qt library paths updated
to your modified static prefix. Preserve all application object files and response
files and the original quoting around paths containing spaces. Create the command's
`Dux Files.app/Contents/MacOS` output directory first. Replace the UI executable in
a copy of the distributed app, replace its `dfiles-native` helper too, retain
its QML/assets/backend, then run the dependency checker again. Alternatively
rebuild the matching application sources
against your modified Qt with a fresh `BUILD_DIR`/`DIST_DIR`.

Exercise the exact object-only relink before a public release. A successful parser
test or shared Qt development build does not verify this step. The unsigned local
app flow does not implement installation information for locked deployment
channels. Preserve the original Qt and host build trees until release obligations
have been reviewed and met; path references in CMake metadata are build-specific.

## Host contract and limitations

CMake must produce target `dfiles-ui` and
`Dux Files.app/Contents/MacOS/Dux Files`, accept `DFILES_BACKEND` pointing at the
CGO-free backend, stage `BUILD_DIR/qml/qs` and preserve original assets/fonts.
`DFILES_REQUIRE_STATIC_QT=ON` must reject a non-static `Qt6::Core`. The host must
statically import the Cocoa platform plugin, every required QML module and image
plugins, including SVG. The Unix Makefiles generator preserves `link.txt` and
application objects for distribution.

The six-module recipe does not build QtWebEngine/QtPdf. Staging replaces only the
macOS PDF preview with CoreGraphics rendering through `NativePdfDocument`, while
retaining the original overlay controls and DCommon page list. ImageIO supplies
image thumbnails, AVFoundation supplies video posters/audio artwork, and generated
backend thumbnails use the existing cache.

The bundled helper handles LaunchServices application associations/defaults,
NSPasteboard file/image data, native Trash, Disk Arbitration/APFS, installed NetFS
protocols, Spotlight and appearance notifications. The Go backend invokes it with
bounded JSON stdin/stdout and `CGO_ENABLED=0`. A single notification helper stays
alive for system events; it does not poll. Other helper requests are short-lived.
The host and backend share native file-selection ownership so clearing an old Dux
selection does not clear another application's newer clipboard contents.

On macOS, staged `Paths` honors explicit `HOME`, `XDG_CACHE_HOME` and
`XDG_STATE_HOME`, with Qt's native locations as fallbacks. This keeps session/cache
data inside isolated test directories; macOS `QStandardPaths` does not otherwise
honor the XDG cache/state overrides. Native-owned daemons use EOF on the host's
stdin pipe to shut down after a host crash, without polling. Headless daemons do
not monitor stdin. CLI-supervised UI exit paths close services and remove runtime
tracking before preserving the UI exit status.

The native folder-handler setting changes LaunchServices only on an explicit user
action. Startup does not make Dux the default. The previous effective folder opener
is saved before changing it; disabling restores it only while Dux still owns the
association. Lookup and setting use `kLSRolesAll`, matching normal folder opening,
not the potentially different viewer-role handler. Restore preserves the previous
effective opener, not each separate role preference. LaunchServices has no atomic
compare-and-swap, so external ownership-change checks are best-effort.
Folder-handler updates hold a separate state-file lock until the request returns;
closing its descriptor releases the lock, including on error paths.
macOS has no supported global replacement for other applications' open/save
panels, so the Linux desktop-portal chooser remains unavailable. Secure privileged
file operations also remain unavailable without a separately designed, signed and
authorized privileged helper; no sudo fallback is provided.

| Area | macOS behavior and boundary |
| --- | --- |
| File operations and archives | APFS regular-file copies use `clonefile`; unsupported/cross-device clones fall back to streaming. Native `copyfile` preserves and checks modes, creation/modification times, flags, ACLs and transferable extended attributes/resource forks without following symlinks; destination ownership stays with the creating user. System-managed `com.apple.provenance` is left to macOS rather than required to match byte-for-byte. Other metadata failures retain the source. Tree moves defer source deletion until all copied content and metadata finish. Immutable/append-only/no-unlink metadata and privileged copies fail explicitly rather than discard protection or ownership. |
| Thumbnails and previews | Space and Quick Look use the system preview panel. ImageIO, CoreGraphics, AVFoundation and cached Quick Look thumbnails remain available; dataless cloud contents are not opened for preview or image decoding. Native image conversion copies color and alpha instead of blending transparent icons/thumbnails into uninitialized memory. |
| Cloud files | The context menu shows local/dataless/downloading/error states. Downloads use the iCloud API or File Provider read coordination. Polling runs only while the status dialog is open and a download is pending/running. Copy/move hold a native coordination accessor until the Go transfer completes. |
| Finder metadata | Context-menu actions add/remove/replace tags, filter the current directory by tag, and create/resolve Finder aliases. Alias resolution does not prompt or mount volumes. Symbolic links remain distinct. |
| Desktop integration | File/Edit/View/Go/Window menus dispatch to the active window. The system sharing picker offers installed sharing services, including AirDrop when available. The Open in Dux Files Service accepts file URLs; outgoing Services receive the current selection on their own pasteboard, while text editors keep text Services. |
| Finder sidebar | Top items, Favorites and Locations are read from the system shared-list API on activation. Hidden/unresolved placeholders are omitted and destinations are deduplicated. Location groups place iCloud before providers/home, then disks, AirDrop and Trash; order within each group follows the shared list. Dux bookmarks remain available, with duplicate native destinations hidden. Failed refreshes retain the last successful list and show one error; a successful empty response clears it. |
| Search | Filename queries fall back to the walker; content queries use Spotlight. A walked filename query reports optional `contentAvailable: true` in its backend event when the native content backend is configured. This is not a guarantee of index coverage. Async errors reach both the results view and CLI; the CLI exits unsuccessfully on a backend error. |
| Devices | Disk Arbitration, `diskutil` and APFS; protected system/backing disks cannot be detached. Encryption actions require encrypted APFS. Eject is supported; device power-off is not. |
| Network | Installed NetFS plugins determine supported SMB/AFP/NFS/WebDAV/FTP schemes. Native mounted responses navigate directly even with an empty `shares` array. Server/share selection and authentication use system UI; no native SFTP or GVfs MTP/camera backend is provided. |
| Clipboard | Native file URLs, ownership-safe cut state and images. External file selections are copied; a foreign clipboard replacement is not cleared. |
| Terminal | Terminal.app and iTerm receive folder-open requests. Alacritty, Ghostty, kitty and WezTerm receive explicit working-directory options; `.app` launches use a new instance so startup arguments are not lost. Other configured executables inherit cwd. Real Alacritty bundle and executable launches were checked with a Unicode/space-containing folder. |
| Games | Native Steam libraries and Heroic GOG, Epic/Legendary and Amazon/Nile installed-game manifests feed the existing sidebar. |

The macOS sidebar uses compact, unbadged template icons. Provider symbols come
from the extension's `CFBundleIcons/CFBundlePrimaryIcon/CFBundleSymbolName`
([Apple's Finder sidebar icon contract](https://developer.apple.com/documentation/fileprovider/setting-the-finder-sidebar-icon)).
The helper resolves an installed owning application by bundle-ID prefix and verifies
the extension ID before loading its symbol; an unavailable symbol uses a cloud glyph.
Standard locations use SF Symbols and installed CoreTypes sidebar artwork. Names
and group titles use the installed Finder localization, with English fallbacks.
No cloud document contents are read to obtain these icons.

Finder exposes no supported complete sidebar-model API. The macOS adapter uses
`LSSharedFileList` for the top section, Favorites and Locations. This API is
deprecated and marked unsupported by Apple; it remains usable on the checked
macOS 27.2 system, but future compatibility is not guaranteed. Its special-item
and group properties, TopSidebarSection identifier and Finder localization/CoreTypes
resource names are implementation details.

Direct `.sfl4` reading was removed: it did not locate the complete Favorites list
and the standalone app encountered archive access denial. The system-list reader
does not open those archive files directly or require archive access to be granted.
It refreshes at startup and when a window becomes active, without polling. On a
failed refresh, existing rows remain usable and the error appears once in the top
section. A later successful response replaces the rows and clears the error.

Resolution does not prompt or mount volumes. iCloud uses the local CloudDocs
directory when present; Shared and AirDrop open their native Finder destinations.
Recent and Trash keep Dux's views, selection, context menus and safe file-operation
behavior. Finder preferences are read only, and no user bookmarks are deleted.

November 6 verification launched the rebuilt app through LaunchServices with an
isolated Dux profile and observed Favorites, iCloud, Google Drive and OneDrive.
All 13 CTest groups passed, including a failing-before/passing-after regression
for initial failure, retained rows after helper errors/invalid replies, recovery
and a successful empty list. The LaunchServices run separately reported an access
denial from `trash.count`; sidebar visibility does not establish Trash permission.

These adapters do not turn Linux-only system services into macOS services. The
portal chooser, privileged helper, SFTP and portable-device limitations above
are explicit unsupported capabilities, not silent success paths.

Actual Cocoa checks on November 1, 2026 verified directory/owner metadata, native
volume enumeration with a protected root, mounted-network enumeration, PDF app
associations, thumbnail cache paths and open/properties/hide/show/quit requests.
A temporary-directory IPC run also verified file/folder creation, copy/move/link,
Unicode rename, permissions, undo/redo, ZIP/tar.gz/tar.xz/tar.zst round trips and
password-protected ZIP extraction. These drivers do not touch existing user files.
Automated checks do not change the user's clipboard, Trash, defaults or mounts.
Finder clipboard interoperability, real Trash restore, external-disk/APFS actions,
network authentication and indexed-content coverage still need opt-in end-to-end
validation. Spotlight does not fabricate filename results for content searches;
macOS privacy grants and index coverage still apply. Finder Trash items without
Dux origin metadata cannot be restored to an invented original path.

November 5, 2026 verification also exercised the actual Cocoa UI with native
mouse/key events: directory and text-file double-clicks, background hide/show,
Command+T, F3, Command+Shift+period, Command+N, Command+Q and physical Control+Q
with two open windows. Quit removed the daemon socket; restart restored tabs,
split view and hidden-file state. Quit shortcuts belong to their active window,
not the windowless ShellRoot, and retain the shared active-operation confirmation.
The separate verification build passed all 13 CTest groups, including native
volume classification, Finder shared-list decoding, tag/alias metadata, directory
creation-time preservation and local cloud-state handling. The changed search,
terminal, IPC, game-discovery and CLI Go packages passed CGO-disabled tests/vet;
the Linux arm64 CLI also cross-compiled. Alacritty bundle/executable launches
opened a real shell in the selected folder. A 120-second hidden run remained
responsive; this does not establish an overnight soak guarantee.

The native-feature checks also displayed the tag editor and tag-filtered listing,
local cloud status, system Quick Look and the system sharing picker. Native menu
dispatch and the incoming Open in Dux Files Service were exercised. An isolated
static-host IPC run preserved data, modes, creation/modification times, ACLs,
extended attributes, resource forks and relative symlinks. Modifying a copied
file left the source unchanged; merging a moved tree preserved directory metadata
before deleting its source. Directory creation times require an explicit
`ATTR_CMN_CRTIME` update after `fcopyfile`; that regression has a native test.
Real cloud hydration and AirDrop delivery to another device remain unverified.

November 6, 2026: provenance validation no longer blocks file copies when macOS
rewrites its system-owned `com.apple.provenance` attribute. The native copy still
runs; no attribute is removed, and other xattrs, including quarantine, retain
their strict verification. Process-dependent provenance changes are also documented
in this [upstream report](https://github.com/docker/for-mac/issues/7636).
The corrected static app completed native Edit > Paste and Command-V operations
with a named pasteboard and temporary files carrying provenance and a user xattr;
file bytes, the user xattr and the original file were preserved. Seven Darwin
file-operation tests and package vet passed with `CGO_ENABLED=0`. The full
file-operation test run could not pass three cross-device cases because no second
test filesystem was available; those cases are not counted as verified.

The local arm64 static build completed with Qt 6.10.0, Apple Clang 21 and
Xcode/macOS SDK 27.2 after the user accepted the Xcode license and installed the
first-launch/Metal components. Qt warns that SDK 27.2 is newer than its tested
SDK 26; that warning remains enabled. No global `xcode-select` change was made.

`dist/macos-arm64/Dux Files.app` passed the system-only dependency checker and
rendered the original Cocoa interface with only system commands in `PATH`.
The separate static verification build passed all nine CTest groups, including
the QML interaction fixtures, isolated session persistence, both UI-crash cleanup
paths and named-pasteboard checks. Native CLI restart and headless stdin EOF were
also exercised. Temporary file/archive IPC operations passed on the static host.
Both executables were relinked from the distributed object files against the
same static Qt prefix in a temporary relocated build, then passed dependency and
Cocoa render checks. A modified-Qt relink remains unverified.

CGO-disabled CLI and vendored shellapp tests passed; the Linux CLI cross-compiled
and CLI vet passed. The full Go suite was not green: three fileops cross-device
tests require a second filesystem, unavailable on this builder while `CI` is set.
No mount was created and those failures were not suppressed.

The binaries declare a macOS 13 minimum, but execution on macOS 13, a clean machine
without development tools, signing/notarization and Gatekeeper remain unverified.
No valid code-signing identity was available on the verification machine.
The local build and system-only `PATH` smoke do not replace those release checks.
