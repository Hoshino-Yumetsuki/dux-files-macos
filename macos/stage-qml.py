#!/usr/bin/env python3
import argparse
import pathlib
import re
import shutil


def stage(source, destination):
    source = source.resolve()
    destination = destination.resolve()
    if not source.is_dir():
        raise ValueError(f"QML source does not exist: {source}")
    if source == destination or source in destination.parents or destination in source.parents:
        raise ValueError("QML source and staging directories must not overlap")
    if destination.exists():
        shutil.rmtree(destination)
    shutil.copytree(source, destination, symlinks=False,
                    ignore=shutil.ignore_patterns(".qmlls.ini", ".claude", "tests", "scripts", "*.py", "*.md"))
    runtime = pathlib.Path(__file__).resolve().parent / "runtime/qml"
    modules = destination / "Modules"
    if modules.is_dir():
        shutil.copyfile(runtime / "FinderPlacesSection.qml", modules / "FinderPlacesSection.qml")
        shutil.copyfile(runtime / "NativeFileTools.qml", modules / "NativeFileTools.qml")
    for directory in [destination, *destination.rglob("*")]:
        if not directory.is_dir():
            continue
        files = sorted(directory.glob("*.qml"))
        if not files:
            continue
        module = ".".join(["qs", *directory.relative_to(destination).parts])
        lines = [f"module {module}"]
        for path in files:
            singleton = bool(re.search(r"^\s*pragma Singleton\b", path.read_text(), re.MULTILINE))
            lines.append(f"{'singleton ' if singleton else ''}{path.stem} 1.0 {path.name}")
        (directory / "qmldir").write_text("\n".join(lines) + "\n")
    shell = destination / "shell.qml"
    if shell.exists():
        text = shell.read_text().replace("    function closeOutcome(force) {", '''    function finishApplicationQuit() {
        FolderHistory.unload();
        DuxFilesService.quit();
    }

    function requestApplicationQuit() {
        if (FileOpsService.activeCount === 0) {
            finishApplicationQuit();
            return;
        }
        showAndFocus();
        const target = windowObject(WindowService.target());
        if (target)
            target.dialogs.confirmQuit(() => root.finishApplicationQuit());
    }

    function closeOutcome(force) {''')
        text = text.replace("            onCloseRequested:", "            onApplicationQuitRequested: root.requestApplicationQuit()\n            onCloseRequested:")
        shell.write_text(text)
    preview = destination / "Modules/PreviewPdf.qml"
    if preview.exists():
        shutil.copyfile(runtime / "PreviewPdf.qml", preview)
    probe = destination / "Services/PdfProbe.qml"
    if probe.exists():
        probe.write_text("import QtQuick\nimport Quickshell\nItem { NativePdfDocument {} }\n")
    portal = destination / "Services/PortalService.qml"
    if portal.exists():
        portal.write_text("pragma Singleton\nimport QtQuick\nimport Quickshell\nSingleton {\n"
            "    readonly property int systemColorScheme: NativePlatform.colorScheme\n"
            "    readonly property bool available: NativePlatform.available\n"
            "    readonly property bool systemPrefersLight: available && systemColorScheme !== 1\n}\n")
    paths = destination / "Common/Paths.qml"
    if paths.exists():
        text = paths.read_text()
        for variable, fallback in (
            ("HOME", "StandardPaths.standardLocations(StandardPaths.HomeLocation)[0]"),
            ("XDG_CACHE_HOME", "StandardPaths.standardLocations(StandardPaths.GenericCacheLocation)[0]"),
            ("XDG_STATE_HOME", "StandardPaths.writableLocation(StandardPaths.GenericStateLocation)"),
        ):
            text = text.replace(fallback, f'(Quickshell.env("{variable}") ? NativePlatform.localUrl(Quickshell.env("{variable}")) : {fallback})')
        paths.write_text(text)
    paths = destination / "DCommon/FileBrowser/FilePaths.qml"
    if paths.exists():
        paths.write_text(paths.read_text().replace("        if (!path)\n            return \"\";",
            "        if (!path)\n            return \"\";\n        if (path.startsWith(\"image://duxmedia/\"))\n            return path;"))
    drag = destination / "Services/DragService.qml"
    if drag.exists():
        drag.write_text(drag.read_text().replace(
            'return drop.urls.map(url => url.toString()).filter(text => text.startsWith("file://")).map(text => FilePaths.fromUrl(text));',
            'return NativePlatform.localPaths(drop.urls);'))
    bridge = destination / "Services/DuxFilesService.qml"
    if bridge.exists():
        bridge.write_text(bridge.read_text().replace('sendRequest("ui.quit", null, null);',
            'sendRequest("ui.quit", null, response => { if (!response.error) Qt.quit(); });'))
    devices = destination / "Modules/DevicesSection.qml"
    if devices.exists():
        text = devices.read_text().replace("import QtQuick\n", "import QtQuick\nimport Quickshell\n")
        text = text.replace(
            "device.mounted || !device.hidden || SettingsData.showSystemPartitions",
            "(device.system || !device.hidden || SettingsData.showSystemPartitions) && !NativePlatform.finderPlaces.some(place => place.path && (device.mountPoints || [device.mountPoint]).includes(place.path))")
        text = text.replace("model: NetworkService.deviceMounts",
            "model: NetworkService.deviceMounts.filter(mount => !NativePlatform.finderPlaces.some(place => place.path === mount.path))")
        devices.write_text(text)
    network = destination / "Modules/NetworkSection.qml"
    if network.exists():
        text = network.read_text().replace("import QtQuick\n", "import QtQuick\nimport Quickshell\n")
        text = text.replace("model: NetworkService.networkMounts",
            "model: NetworkService.networkMounts.filter(mount => !NativePlatform.finderPlaces.some(place => place.path === mount.path))")
        network.write_text(text)
    window = destination / "Modules/MainWindow.qml"
    if window.exists():
        text = window.read_text().replace("import Quickshell\n", "import Quickshell\nimport Quickshell.Widgets\n")
        text = text.replace("    color: Theme.windowSurface", '    color: "transparent"')
        text = text.replace("    onWidthChanged: {", "    property bool nativeSizeReady: false\n    onWidthChanged: {")
        text = text.replace("if (!maximized)\n            SessionData.window", "if (nativeSizeReady && !maximized)\n            SessionData.window")
        text = text.replace("    Component.onCompleted: {\n        keepOwnBrowsing();", "    Component.onCompleted: {\n        nativeSizeReady = true;\n        SessionData.windowWidth = width;\n        SessionData.windowHeight = height;\n        keepOwnBrowsing();")
        text = text.replace("readonly property bool dialogOpen: dialogs.anyOpen", "readonly property bool dialogOpen: dialogs.anyOpen || nativeFileTools.anyOpen")
        text = text.replace("        blurHeight: window.height", "        blurHeight: window.height\n        blurRadius: window.maximized || window.visibility === Window.FullScreen ? 0 : Theme.windowRadius")
        text = text.replace("    Item {\n        id: surface", "    ClippingRectangle {\n        id: surface\n\n        radius: window.maximized || window.visibility === Window.FullScreen ? 0 : Theme.windowRadius\n        color: Theme.windowSurface")
        text = text.replace("        onWindowActiveChanged: {", "        onWindowActiveChanged: {\n            if (windowActive)\n                NativePlatform.refreshFinderPlaces();")
        text = text.replace("    signal closeRequested(bool force)", '''    signal applicationQuitRequested()

    Shortcut {
        sequences: ["Ctrl+Q", "Meta+Q"]
        context: Qt.WindowShortcut
        onActivated: window.applicationQuitRequested()
    }

    signal closeRequested(bool force)''')
        text = text.replace('    signal applicationQuitRequested()', '''    readonly property alias nativeFileTools: nativeFileTools
    NativeFileTools { id: nativeFileTools; mainWindow: window }
    function nativeMenuEnabled(id) {
        if (id === "settings" || id === "newWindow") return true;
        if (dialogOpen) return false;
        if (textEditing && ["cut", "copy", "paste", "selectAll"].includes(id)) return true;
        if (["open", "quickLook", "shareFiles", "copy", "cut", "trash", "properties", "finderTags"].includes(id))
            return selectedPaths().length > 0 && actions.scopedAction(id) !== "";
        if (id === "rename") return selectedPaths().length === 1 && actions.scopedAction(id) !== "";
        if (id === "paste") return ClipboardService.hasPaste && actions.scopedAction(id) !== "";
        if (id === "back") return activePane.canGoBack;
        if (id === "forward") return activePane.canGoForward;
        return actions.scopedAction(id) !== "";
    }
    function nativeMenuAction(id) {
        if (!nativeMenuEnabled(id)) return;
        if (textEditing && ["cut", "copy", "paste", "selectAll"].includes(id)) {
            const editor = surface.focusItem;
            if (typeof editor[id] === "function") editor[id]();
        } else actions.runAction(id);
    }
    Binding {
        target: window.activePane?.body?.directory ?? null
        property: "nativePathFilter"
        value: nativeFileTools.filterActive ? nativeFileTools : null
    }
    Connections {
        target: nativeFileTools
        function onFilterRevisionChanged() { window.activePane?.body?.directory?._refilter(); }
    }
    signal applicationQuitRequested()''')
        window.write_text(text)
    settings_data = destination / "Common/SettingsData.qml"
    if settings_data.exists():
        text = settings_data.read_text().replace("    property alias gtkBookmarksOffered:",
            "    readonly property bool nativeSettingsLoaded: settingsFile._loaded\n"
            "    property alias fullDiskAccessOffered: adapter.fullDiskAccessOffered\n"
            "    property alias gtkBookmarksOffered:")
        text = text.replace("            property bool gtkBookmarksOffered:",
            "            property bool fullDiskAccessOffered: false\n"
            "            property bool gtkBookmarksOffered:")
        settings_data.write_text(text)
    settings = destination / "Modules/Settings/SettingsContent.qml"
    if settings.exists():
        text = settings.read_text()
        start = text.index("    function setFileManager(enabled) {")
        end = text.index("    function collectRows(", start)
        text = text[:start] + (runtime / "FileManagerSettings.js").read_text() + "\n" + text[end:]
        text = text.replace("checked: SettingsData.registerFileManager", "checked: root.fileManagerStatus.enabled === true")
        text = text.replace('I18n.tr("Register as the session file manager", "setting label for owning org.freedesktop.FileManager1")',
            'I18n.tr("Use Dux Files to open folders", "macOS default folder handler setting label")')
        text = text.replace("enabled: root.fileManagerStatus.available !== false", "enabled: root.fileManagerStatus.available === true")
        settings.write_text(text)
    shortcuts = destination / "Common/Shortcuts.qml"
    if shortcuts.exists():
        text = shortcuts.read_text()
        text = text.replace('"id": "togglePreview",\n            "keys": ["Space"]', '"id": "quickLook",\n            "keys": ["Space"]')
        for before, after in (
            ('"keys": ["Alt+Up", "Backspace"]', '"keys": ["Alt+Up", "Backspace", "Ctrl+Up"]'),
            ('"keys": ["Del"]', '"keys": ["Del", "Ctrl+Backspace"]'),
            ('"keys": ["Shift+Del"]', '"keys": ["Shift+Del", "Ctrl+Shift+Backspace"]'),
            ('"keys": ["Ctrl+H"]', '"keys": ["Ctrl+Shift+."]'),
            ('"keys": ["Ctrl+Tab", "Ctrl+PgDown"]', '"keys": ["Meta+Tab", "Ctrl+PgDown"]'),
            ('"keys": ["Ctrl+Shift+Tab", "Ctrl+PgUp"]', '"keys": ["Meta+Shift+Tab", "Ctrl+PgUp"]'),
        ):
            text = text.replace(before, after)
        shortcuts.write_text(text)
    actions = destination / "Modules/WindowActions.qml"
    if actions.exists():
        text = actions.read_text().replace('    function runAction(id) {', '''    function runAction(id) {
        if (id === "quickLook" || id === "shareFiles") {
            const ok = id === "quickLook" ? NativePlatform.previewFiles(mainWindow, mainWindow.selectedPaths()) : NativePlatform.shareFiles(mainWindow, mainWindow.selectedPaths());
            if (!ok) ToastService.info(I18n.tr("Select locally available files first", "native preview or share unavailable"));
            return;
        }
        if (mainWindow.nativeFileTools.runAction(id)) return;''')
        actions.write_text(text.replace('import QtQuick\n', 'import QtQuick\nimport Quickshell\n'))
    opening = destination / "Modules/FileOpenFlow.qml"
    if opening.exists():
        text = opening.read_text().replace('    function activate(entry) {', '''    function activate(entry) {
        if (!entry || activePane.trashActive || entry.isDir) { activateOrdinary(entry); return; }
        NativePlatform.request("metadata.aliasInfo", { path: entry.path }, result => {
            if (result.error) { reportError(result); return; }
            if (!result.isAlias) { activateOrdinary(entry); return; }
            NativePlatform.request("metadata.aliasResolve", { path: entry.path }, target => {
                if (target.error) { reportError(target); return; }
                if (target.isDirectory) activePane.navigate(target.path);
                else AppsService.open([target.path], response => flow.reportError(response));
            });
        });
    }
    function activateOrdinary(entry) {''')
        opening.write_text(text)
    context = destination / "Modules/FileContextMenu.qml"
    if context.exists():
        text = context.read_text().replace('    readonly property var itemRows: [', '''    readonly property var itemRows: [
        { "id": "quickLook", "icon": "visibility", "label": I18n.tr("Quick Look", "native file preview"), "enabled": selectionCount > 0 },
        { "id": "shareFiles", "icon": "share", "label": I18n.tr("Share…", "system sharing picker"), "enabled": selectionCount > 0 },
        { "id": "finderTags", "icon": "label", "label": I18n.tr("Finder tags…", "edit Finder tags"), "enabled": selectionCount > 0 },
        { "id": "createFinderAlias", "icon": "link", "label": I18n.tr("Make alias", "create Finder alias"), "enabled": selectionCount === 1 },
        { "id": "resolveFinderAlias", "icon": "open_in_new", "label": I18n.tr("Open alias target", "resolve Finder alias"), "enabled": selectionCount === 1 },
        { "id": "cloudStatus", "icon": "cloud", "label": I18n.tr("Cloud status…", "cloud file status"), "enabled": selectionCount === 1 },
        { "id": "cloudDownload", "icon": "download", "label": I18n.tr("Download cloud file…", "download cloud file"), "enabled": selectionCount === 1 },
        { "id": "separator" },''')
        text = text.replace('    readonly property var backgroundRows: [', '''    readonly property var backgroundRows: [
        { "id": "finderTagFilter", "icon": "label", "label": I18n.tr("Filter by Finder tag…", "filter directory by Finder tag"), "enabled": true },''')
        context.write_text(text)
    directory = destination / "DCommon/FileBrowser/DirectoryModel.qml"
    if directory.exists():
        text = directory.read_text().replace('    property string nameFilter: ""', '    property var nativePathFilter: null\n    onNativePathFilterChanged: _refilter()\n    property string nameFilter: ""')
        text = text.replace('    function _passes(entry) {', '    function _passes(entry) {\n        if (nativePathFilter && !nativePathFilter.matchesPath(entry.path)) return false;')
        directory.write_text(text)
    pane = destination / "Modules/FilesPane.qml"
    if pane.exists():
        text = pane.read_text().replace(
            '        switch (event.key) {\n        case Qt.Key_Return:',
            '        if (event.key === Qt.Key_Down && event.modifiers === Qt.ControlModifier) {\n'
            '            event.accepted = openSelection();\n            return;\n        }\n'
            '        switch (event.key) {\n        case Qt.Key_Return:')
        text = text.replace('if ((event.modifiers & Qt.AltModifier) === 0)',
            'if ((event.modifiers & Qt.AltModifier) === 0 && event.modifiers !== Qt.ControlModifier)')
        pane.write_text(text)
    body = destination / "DCommon/FileBrowser/FileViewBody.qml"
    if body.exists():
        body.write_text(body.read_text().replace(
            'if ((modifiers & Qt.AltModifier) !== 0)\n            return;',
            'if ((modifiers & Qt.AltModifier) !== 0 || (modifiers === Qt.ControlModifier && (event.key === Qt.Key_Up || event.key === Qt.Key_Down)))\n            return;'))
    sidebar = destination / "Modules/Sidebar.qml"
    if sidebar.exists():
        text = sidebar.read_text().replace(
            'const key = event.key === Qt.Key_F10 && shift ? Qt.Key_Menu : event.key;',
            'const key = event.key === Qt.Key_Backspace && event.modifiers === Qt.ControlModifier ? Qt.Key_Delete : event.key === Qt.Key_F10 && shift ? Qt.Key_Menu : event.key;')
        finder_sections = ""
        for section in ("top", "favorites", "locations"):
            finder_sections += ('        FinderPlacesSection {\n'
                '            width: sections.width\n'
                '            currentPath: sidebar.currentPath\n'
                '            devicesController: devicesSection\n'
                f'            sectionName: "{section}"\n'
                '            onTrashMenuRequested: (x, y) => { const p = mapToItem(sidebar, x, y); sidebar.trashMenuRequested(p.x, p.y); }\n'
                '            onRecentMenuRequested: (x, y) => { const p = mapToItem(sidebar, x, y); sidebar.recentMenuRequested(p.x, p.y); }\n'
                '            onNavigateRequested: path => sidebar.navigateRequested(path)\n'
                '            onTabRequested: path => sidebar.tabRequested(path)\n'
                '            onDropRequested: (path, drop) => sidebar.dropRequested(path, drop)\n'
                '        }\n\n')
        text = text.replace("        BookmarksSection {", finder_sections + "        BookmarksSection {")
        text = text.replace("        DevicesSection {", "        DevicesSection {\n            id: devicesSection")
        sidebar.write_text(text)
    bookmarks = destination / "Modules/BookmarksSection.qml"
    if bookmarks.exists():
        text = bookmarks.read_text().replace("import QtQuick\n", "import QtQuick\nimport Quickshell\n")
        text = text.replace("entry.path !== RecentService.path || RecentService.shown",
            "(entry.path !== RecentService.path || RecentService.shown) && !NativePlatform.finderPlaces.some(place => place.path === entry.path || (entry.path === RecentService.path && place.kind === \"recent\") || (entry.path === TrashService.path && place.kind === \"trash\"))")
        text = text.replace("    title: I18n.tr(\"Places\"", "    visible: bookmarks.length > 0\n\n    title: I18n.tr(\"Places\"")
        bookmarks.write_text(text)
    row = destination / "DCommon/FileBrowser/FilePlaceRow.qml"
    if row.exists():
        text = row.read_text().replace('    property string iconName: ""', '    property string iconName: ""\n    property string nativeIcon: ""\n    property color nativeIconColor: contentColor')
        text = text.replace("implicitHeight: Math.max(FileBrowserMetrics.sidebarRowHeight, labels.implicitHeight + Style.spacingM)", 'implicitHeight: row.nativeIcon !== "" ? Math.max(Style.buttonHeightXS, labels.implicitHeight + Style.spacingXS) : Math.max(FileBrowserMetrics.sidebarRowHeight, labels.implicitHeight + Style.spacingM)')
        text = text.replace("Layout.preferredWidth: FileBrowserMetrics.sidebarIconSize", 'Layout.preferredWidth: row.nativeIcon !== "" ? FileBrowserMetrics.sidebarGlyphSize : FileBrowserMetrics.sidebarIconSize')
        text = text.replace("Layout.preferredHeight: FileBrowserMetrics.sidebarIconSize", 'Layout.preferredHeight: row.nativeIcon !== "" ? FileBrowserMetrics.sidebarGlyphSize : FileBrowserMetrics.sidebarIconSize')
        text = text.replace("color: row.showAction ? Style.primaryContainer : Style.withAlpha(Style.primary, Style.tonalTintAlpha)", 'color: row.nativeIcon !== "" ? "transparent" : row.showAction ? Style.primaryContainer : Style.withAlpha(Style.primary, Style.tonalTintAlpha)')
        text = text.replace("visible: !row.busy && !row.showAction\n                    iconName:", 'visible: !row.busy && !row.showAction && row.nativeIcon === ""\n                    iconName:')
        text = text.replace("                FileIcon {", '''                DSVGIcon {
                    anchors.centerIn: parent
                    visible: !row.busy && !row.showAction && row.nativeIcon !== ""
                    source: row.nativeIcon
                    size: FileBrowserMetrics.sidebarGlyphSize
                    colorOverride: row.nativeIconColor
                }

                FileIcon {''')
        row.write_text(text)
    section = destination / "DCommon/FileBrowser/FilePlaceSection.qml"
    if section.exists():
        text = section.read_text().replace("        text: section.title", '        visible: section.title !== ""\n        text: section.title')
        text = text.replace("color: Style.primary", "color: Style.onSurfaceVariant").replace("font.pixelSize: Style.fontSizeMedium", "font.pixelSize: Style.fontSizeSmall")
        section.write_text(text)
    for name, before, after in (
        ("DCommon/FileBrowser/PathBar.qml", "anchors.verticalCenter: parent.verticalCenter\n                    spacing: FileBrowserMetrics.pathBarSpacing", "anchors.verticalCenter: pills.verticalCenter\n                    spacing: FileBrowserMetrics.pathBarSpacing"),
        ("DCommon/Widgets/DTabBar.qml", "anchors.verticalCenter: parent.verticalCenter", "anchors.verticalCenter: tabRow.verticalCenter"),
    ):
        path = destination / name
        if path.exists():
            path.write_text(path.read_text().replace(before, after))
    controls = destination / "DCommon/Widgets/FloatingWindowControls.qml"
    text = controls.read_text()
    text = text.replace('ownDecorations && Compositor.desktop === "kde"',
                        'ownDecorations && (Qt.platform.os === "osx" || Compositor.desktop === "kde")')
    controls.write_text(text)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description="Stage the original Dux QML for Qt's standard import loader")
    parser.add_argument("source", type=pathlib.Path)
    parser.add_argument("destination", type=pathlib.Path)
    args = parser.parse_args()
    stage(args.source.resolve(), args.destination.resolve())
