import QtQuick
import Quickshell
import qs.Common
import qs.Services
import qs.DCommon.Widgets

Item {
    id: root
    required property var mainWindow
    readonly property bool anyOpen: fullDiskAccessDialog.visible || tagDialog.visible || filterDialog.visible || cloudDialog.visible || aliasDialog.visible
    property bool filterActive: false
    property int filterRevision: 0
    property string filterDirectory: ""
    property string filterTag: ""
    property var filteredPaths: ({})
    property int filterRequest: 0
    property int filterDialogRequest: 0
    property var targets: []
    property int tagRequest: 0
    property bool tagBusy: false
    property string currentTags: ""
    property string aliasSource: ""
    property int aliasRequest: 0
    property string cloudPath: ""
    property var cloudResult: ({})
    property int cloudRequest: 0
    property bool cloudBusy: false
    property bool cloudWaiting: false
    property double cloudDeadline: 0
    readonly property bool fullDiskAccessOfferPending: mainWindow.primary && mainWindow.visible && SettingsData.nativeSettingsLoaded && !SettingsData.fullDiskAccessOffered

    onFullDiskAccessOfferPendingChanged: Qt.callLater(offerFullDiskAccess)
    Component.onCompleted: Qt.callLater(offerFullDiskAccess)

    function offerFullDiskAccess() {
        if (!fullDiskAccessOfferPending)
            return;
        SettingsData.fullDiskAccessOffered = true;
        fullDiskAccessDialog.open();
    }

    function matchesPath(path) {
        return !filterActive || mainWindow.currentPath !== filterDirectory || !!filteredPaths[path];
    }
    function report(result) {
        if (result && result.error) {
            ToastService.info(result.error);
            return true;
        }
        return false;
    }
    function names(value) {
        return value.split(",").map(s => s.trim()).filter(s => s !== "");
    }
    function editTags(mode) {
        if (tagBusy) return;
        const request = ++tagRequest;
        tagBusy = true;
        NativePlatform.request("metadata.tagsEdit", { paths: targets, tags: names(tagInput.text), mode: mode }, result => {
            if (request !== tagRequest || !tagDialog.visible) return;
            tagBusy = false;
            if (report(result)) return;
            if (result.errors && result.errors.length) {
                const errors = result.errors.map(e => e.path + ": " + e.error).join("\n");
                tagDialog.supportingText = currentTags + "\n" + errors;
                tagBusy = true;
                NativePlatform.request("metadata.tagsRead", { paths: targets }, refreshed => {
                    if (request !== tagRequest || !tagDialog.visible) return;
                    tagBusy = false;
                    if (!refreshed.error) currentTags = refreshed.items.map(item => item.path + ": " + item.tags.join(", ")).join("\n");
                    tagDialog.supportingText = currentTags + "\n" + errors;
                });
                if (filterActive) refreshFilter();
                return;
            }
            tagDialog.close();
            if (filterActive) refreshFilter();
        });
    }
    function refreshFilter() {
        if (!filterActive) return;
        const folder = mainWindow.currentPath;
        const tag = filterTag;
        const request = ++filterRequest;
        filterDirectory = folder;
        filteredPaths = ({});
        filterRevision++;
        NativePlatform.request("metadata.tagsList", { path: folder }, result => {
            if (request !== filterRequest || !filterActive || folder !== mainWindow.currentPath || tag !== filterTag) return;
            if (report(result)) {
                filterActive = false;
                filterRevision++;
                return;
            }
            const paths = {};
            for (const item of result.items || [])
                if (item.tags.includes(tag)) paths[item.path] = true;
            filteredPaths = paths;
            filterRevision++;
        });
    }
    function applyFilter() {
        filterTag = filterInput.text.trim();
        filterActive = filterTag !== "";
        filterRequest++;
        filterRevision++;
        filterDialog.close();
        if (filterActive) refreshFilter();
    }
    function loadCloud(method) {
        if (cloudBusy || !cloudDialog.visible) return;
        const request = ++cloudRequest;
        const path = cloudPath;
        cloudBusy = true;
        NativePlatform.request(method, { path: path }, result => {
            if (request !== cloudRequest || !cloudDialog.visible || path !== cloudPath) return;
            cloudBusy = false;
            if (result.error && result.code) {
                cloudWaiting = false;
                cloudResult = { state: "error", error: result.error };
                return;
            }
            if (method === "cloud.download" && result.state === "dataless") {
                cloudWaiting = true;
                cloudDeadline = Date.now() + 30000;
            } else if (result.state !== "dataless") {
                cloudWaiting = false;
            } else if (cloudWaiting && Date.now() >= cloudDeadline) {
                cloudWaiting = false;
                result = Object.assign({}, result, { error: I18n.tr("Download has not started yet. Check again later.", "cloud download start timeout") });
            }
            cloudResult = result;
        });
    }
    function cloudState() {
        switch (cloudResult.state) {
        case "local": return I18n.tr("Available locally", "cloud file status");
        case "dataless": return cloudWaiting ? I18n.tr("Waiting for download to start", "cloud download pending status") : I18n.tr("Not downloaded", "cloud file status");
        case "downloading": return I18n.tr("Downloading", "cloud file status");
        case "error": return I18n.tr("Unavailable", "cloud file status");
        default: return I18n.tr("Loading…", "cloud status loading");
        }
    }
    function runAction(id) {
        const paths = mainWindow.selectedPaths().slice();
        switch (id) {
        case "finderTags": {
            if (!paths.length) return true;
            targets = paths;
            tagInput.text = "";
            tagBusy = true;
            const request = ++tagRequest;
            currentTags = I18n.tr("Loading tags…", "Finder tags loading status");
            tagDialog.supportingText = currentTags;
            tagDialog.open();
            NativePlatform.request("metadata.tagsRead", { paths: paths }, result => {
                if (request !== tagRequest || !tagDialog.visible) return;
                tagBusy = false;
                if (report(result)) { tagDialog.close(); return; }
                currentTags = result.items.map(item => item.path + ": " + item.tags.join(", ")).join("\n");
                tagDialog.supportingText = currentTags;
            });
            return true;
        }
        case "finderTagFilter": {
            filterInput.text = filterTag;
            filterDialog.supportingText = I18n.tr("Loading tags…", "Finder tags loading status");
            filterDialog.open();
            const folder = mainWindow.currentPath;
            const request = ++filterDialogRequest;
            NativePlatform.request("metadata.tagsList", { path: folder }, result => {
                if (request !== filterDialogRequest || !filterDialog.visible || folder !== mainWindow.currentPath) return;
                if (report(result)) return;
                const available = [];
                for (const item of result.items || [])
                    for (const tag of item.tags) if (!available.includes(tag)) available.push(tag);
                filterDialog.supportingText = available.length ? available.sort().join(", ") : I18n.tr("No tags in this folder", "empty Finder tag filter");
            });
            return true;
        }
        case "createFinderAlias":
            if (paths.length !== 1) return true;
            aliasSource = paths[0];
            aliasDialog.ask(paths[0].substring(paths[0].lastIndexOf("/") + 1) + I18n.tr(" alias", "suffix for a new Finder alias"));
            return true;
        case "resolveFinderAlias": {
            if (paths.length !== 1) return true;
            const request = ++aliasRequest;
            const pane = mainWindow.activePane;
            NativePlatform.request("metadata.aliasResolve", { path: paths[0] }, result => {
                if (request !== aliasRequest || pane !== mainWindow.activePane || mainWindow.selectedPaths().length !== 1 || mainWindow.selectedPaths()[0] !== paths[0]) return;
                if (report(result)) return;
                if (result.isDirectory) pane.navigate(result.path);
                else AppsService.open([result.path], response => report(response));
            });
            return true;
        }
        case "cloudStatus":
        case "cloudDownload":
            if (paths.length !== 1) return true;
            cloudRequest++;
            cloudBusy = false;
            cloudWaiting = false;
            cloudPath = paths[0];
            cloudResult = {};
            cloudDialog.open();
            loadCloud(id === "cloudDownload" ? "cloud.download" : "cloud.status");
            return true;
        default: return false;
        }
    }
    Connections {
        target: root.mainWindow
        function onCurrentPathChanged() {
            root.filterDialogRequest++;
            if (filterDialog.visible) filterDialog.close();
            if (root.filterActive) root.refreshFilter();
        }
    }
    OverlayDialog {
        id: fullDiskAccessDialog
        objectName: "fullDiskAccessDialog"
        title: I18n.tr("Allow Full Disk Access", "macOS first launch permission guidance title")
        supportingText: I18n.tr("Dux Files recommends Full Disk Access to manage protected files and read the Trash. This permission allows access to data from other apps, backups, and other users on this Mac.", "macOS full disk access scope explanation")
            + "\n\n" + I18n.tr("Open System Settings → Privacy & Security → Full Disk Access. Enable Dux Files, or use + to add this copy of Dux Files.app if it is not listed. Then quit and reopen Dux Files.", "macOS manual full disk access instructions")
            + "\n\n" + I18n.tr("You can continue without granting access. Protected locations may remain unavailable.", "macOS full disk access optional permission explanation")
        onAccepted: openSettings()
        function openSettings() {
            if (!Qt.openUrlExternally("x-apple.systempreferences:com.apple.preference.security?Privacy_AllFiles")) {
                ToastService.info(I18n.tr("Could not open System Settings. Open Privacy & Security → Full Disk Access manually.", "macOS permission settings launch failure"));
                return;
            }
            close();
        }
        actions: [
            TextButton { text: I18n.tr("Not now", "skip macOS full disk access guidance"); onClicked: fullDiskAccessDialog.close() },
            DButton { text: I18n.tr("Open System Settings", "open macOS full disk access settings"); backgroundColor: Theme.primary; textColor: Theme.primaryText; onClicked: fullDiskAccessDialog.openSettings() }
        ]
    }
    NameDialog {
        id: aliasDialog
        title: I18n.tr("Create Finder alias", "Finder alias naming dialog")
        acceptText: I18n.tr("Create", "create Finder alias")
        onSubmitted: name => {
            if (name.includes("/") || name === "." || name === "..") {
                ToastService.info(I18n.tr("Enter a valid file name", "invalid Finder alias name"));
                return;
            }
            const source = root.aliasSource;
            const destination = source.substring(0, source.lastIndexOf("/") + 1) + name;
            NativePlatform.request("metadata.aliasCreate", { path: source, destination: destination }, result => {
                if (!root.report(result)) ToastService.info(I18n.tr("Alias created", "Finder alias creation confirmation"));
            });
        }
    }
    OverlayDialog {
        id: tagDialog
        title: I18n.tr("Finder tags", "Finder tags dialog title")
        maximumWidth: Theme.dialogMaxWidth
        onClosed: { root.tagRequest++; root.tagBusy = false; }
        DTextField { id: tagInput; width: parent.width; outlined: true; labelText: I18n.tr("Tags, separated by commas", "Finder tag entry") }
        actions: [
            TextButton { text: I18n.tr("Cancel", "cancel dialog"); onClicked: tagDialog.close() },
            TextButton { text: I18n.tr("Remove", "remove Finder tags"); enabled: !root.tagBusy; onClicked: root.editTags("remove") },
            TextButton { text: I18n.tr("Replace all", "replace all Finder tags"); enabled: !root.tagBusy; onClicked: root.editTags("replace") },
            DButton { text: I18n.tr("Add", "add Finder tags"); enabled: !root.tagBusy; backgroundColor: Theme.primary; textColor: Theme.primaryText; onClicked: root.editTags("add") }
        ]
    }
    OverlayDialog {
        id: filterDialog
        title: I18n.tr("Filter by Finder tag", "Finder tag filter title")
        maximumWidth: Theme.dialogMaxWidth
        onClosed: root.filterDialogRequest++
        DTextField { id: filterInput; width: parent.width; outlined: true; labelText: I18n.tr("Tag", "Finder tag filter input") }
        actions: [
            TextButton { text: I18n.tr("Cancel", "cancel dialog"); onClicked: filterDialog.close() },
            TextButton { text: I18n.tr("Clear filter", "clear Finder tag filter"); onClicked: { root.filterActive = false; root.filterTag = ""; root.filterRequest++; root.filterRevision++; filterDialog.close(); } },
            DButton { text: I18n.tr("Filter", "apply Finder tag filter"); backgroundColor: Theme.primary; textColor: Theme.primaryText; onClicked: root.applyFilter() }
        ]
    }
    OverlayDialog {
        id: cloudDialog
        title: I18n.tr("Cloud file", "cloud file status dialog")
        supportingText: root.cloudPath + "\n" + root.cloudState() + (root.cloudResult.error ? "\n" + root.cloudResult.error : "") + (root.cloudResult.progress !== undefined ? " " + Math.round(root.cloudResult.progress * 100) + "%" : "")
        onClosed: { root.cloudRequest++; root.cloudBusy = false; root.cloudWaiting = false; }
        actions: [
            TextButton { text: I18n.tr("Close", "close dialog"); onClicked: cloudDialog.close() },
            DButton { text: I18n.tr("Download", "download cloud file"); enabled: !root.cloudBusy && !root.cloudWaiting && root.cloudResult.state !== "downloading"; backgroundColor: Theme.primary; textColor: Theme.primaryText; onClicked: root.loadCloud("cloud.download") }
        ]
    }
    Timer { interval: 2000; repeat: true; running: cloudDialog.visible && !root.cloudBusy && (root.cloudWaiting || root.cloudResult.state === "downloading"); onTriggered: root.loadCloud("cloud.status") }
}
