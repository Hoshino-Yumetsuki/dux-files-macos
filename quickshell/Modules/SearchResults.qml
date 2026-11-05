pragma ComponentBehavior: Bound

import QtQuick
import qs.Common
import qs.Services
import qs.DCommon.Widgets
import qs.DCommon.FileBrowser

Rectangle {
    id: results

    required property SearchModel model
    property string viewMode: "list"
    property int gridZoom: 1
    property int listZoom: 0
    property var columnWidths: ({})
    property bool previewOpen: false
    property real previewWidth: 0
    property var cutSet: ({})
    property bool singleClickActivates: false
    property bool typeAheadEnabled: true
    property bool dragEnabled: true
    property bool countFolders: true

    readonly property alias selection: selectionModel
    readonly property alias body: viewBody
    readonly property alias chips: chips
    readonly property string summary: {
        if (model.error !== "")
            return model.error;
        const notes = [];
        switch (true) {
        case model.loading:
            notes.push(I18n.tr("Searching…", "search results header while the daemon walks the tree"));
            break;
        case model.truncated:
            notes.push(I18n.tr("Showing the first %1 results", "search results header when the cap was hit").arg(model.count));
            break;
        case model.count === 0:
            notes.push(I18n.tr("No results", "search results header when nothing matched"));
            break;
        default:
            notes.push(FileFormat.count(model.count));
            break;
        }
        if (model.unreadable > 0)
            notes.push(I18n.tr("%1 folders could not be read", "search results header note").arg(model.unreadable));
        if (model.partial)
            notes.push(I18n.tr("Some matches may be missing", "search results header note when the index could not return every candidate"));
        if (model.servedBy === "index") {
            notes.push(model.indexPhase === "idle" || model.indexPhase === "" || model.indexPhase === "spotlight" ? I18n.tr("From the index", "search results header note when indexed search served the search") : I18n.tr("Index is still building, results may be incomplete", "search results header note while danksearch is indexing"));
            if (model.depthLimit > 0)
                notes.push(I18n.tr("%1 levels deep", "search results header note giving how far below the folder the index reaches").arg(model.depthLimit));
        }
        return notes.join(" · ");
    }
    property bool indexAvailable: false
    readonly property string scopeText: {
        switch (chips.scope) {
        case "home":
            return Paths.shortenHome(Paths.strip(Paths.home));
        case "everywhere":
            return I18n.tr("Everywhere", "search scope chip covering home and every mounted local drive");
        default:
            return Paths.shortenHome(results.model.rootPath);
        }
    }

    signal activateRequested(var entry)
    signal itemMenuRequested(int index, real pointX, real pointY, int modifiers)
    signal dragRequested(var paths)
    signal renameRequested(string target, string name)
    signal zoomRequested(string viewMode, int level)
    signal columnResizeRequested(string id, real width)
    signal previewToggleRequested
    signal previewResized(real width)
    signal focusRequested
    signal tabRequested(string path)
    signal closeRequested
    signal filtersChanged

    function focusBody() {
        viewBody.focusBody();
    }

    radius: FilesMetrics.paneRadius
    color: Theme.foregroundColor(Theme.cardSurface, true)

    SelectionModel {
        id: selectionModel
    }

    Item {
        id: header

        anchors.top: parent.top
        anchors.left: parent.left
        anchors.right: parent.right
        anchors.topMargin: FilesMetrics.paneBodyMargin
        anchors.leftMargin: FilesMetrics.paneBodyMarginH
        anchors.rightMargin: FilesMetrics.paneBodyMarginH
        height: Math.max(FileBrowserMetrics.pathPillHeight, titleColumn.implicitHeight)

        Column {
            id: titleColumn

            anchors.left: parent.left
            anchors.right: closeButton.left
            anchors.rightMargin: Theme.spacingS
            anchors.verticalCenter: parent.verticalCenter
            spacing: Theme.spacingXXS

            StyledText {
                width: parent.width
                text: results.model.query !== "" ? I18n.tr("Results for “%1” in %2", "search results header, query then folder").arg(results.model.query).arg(results.scopeText) : I18n.tr("Results in %1", "search results header without a query").arg(results.scopeText)
                color: Theme.surfaceText
                font.pixelSize: Theme.fontSizeMedium
                font.weight: Theme.fontWeightMedium
                elide: Text.ElideMiddle
            }

            StyledText {
                width: parent.width
                text: results.summary
                color: Theme.surfaceVariantText
                font.pixelSize: Theme.fontSizeSmall
                elide: Text.ElideRight
            }
        }

        DActionButton {
            id: closeButton

            anchors.right: parent.right
            anchors.verticalCenter: parent.verticalCenter
            buttonSize: Theme.buttonHeightXS
            iconName: "close"
            iconSize: Theme.iconSizeSmall
            iconColor: Theme.surfaceVariantText
            tooltipText: I18n.tr("Close", "dialog button to close")
            onClicked: results.closeRequested()
        }
    }

    SearchChips {
        id: chips

        anchors.top: header.bottom
        anchors.topMargin: Theme.spacingS
        anchors.left: parent.left
        anchors.right: parent.right
        anchors.leftMargin: FilesMetrics.paneBodyMarginH
        anchors.rightMargin: FilesMetrics.paneBodyMarginH
        contentsAvailable: results.indexAvailable
        onChanged: results.filtersChanged()
    }

    Connections {
        target: results.model

        function onServedByChanged() {
            if (results.model.servedBy === "")
                return;
            if (chips.direct)
                return;
            results.indexAvailable = results.model.contentAvailable;
            if (!results.indexAvailable)
                chips.contents = false;
        }
    }

    FileViewBody {
        id: viewBody

        anchors.top: chips.bottom
        anchors.topMargin: Theme.spacingS
        anchors.bottom: parent.bottom
        anchors.left: parent.left
        anchors.right: parent.right
        anchors.bottomMargin: FilesMetrics.paneBodyMargin
        anchors.leftMargin: FilesMetrics.paneBodyMarginH
        anchors.rightMargin: FilesMetrics.paneBodyMarginH + drawer.width
        focus: true
        directory: results.model
        selection: selectionModel
        viewMode: results.viewMode === "columns" ? "list" : results.viewMode
        gridZoom: results.gridZoom
        listZoom: results.listZoom
        listColumns: ["location", "size", "modified"]
        columnWidths: results.columnWidths
        previewKeys: true
        sortKey: results.model.sortKey
        sortDescending: results.model.sortDescending
        columnSortKeys: ({
                "location": "location"
            })
        showHidden: chips.includeHidden
        dragEnabled: results.dragEnabled
        dropEnabled: false
        showSelectionFooter: true
        emptyText: ""
        errorText: results.model.error
        surfaceColor: results.color
        cutSet: results.cutSet
        singleClickActivates: results.singleClickActivates
        typeAheadEnabled: results.typeAheadEnabled
        countFolders: results.countFolders
        nameHighlight: results.model.query
        onActivateRequested: entry => results.activateRequested(entry)
        onItemMenuRequested: (index, pointX, pointY, modifiers) => {
            results.focusRequested();
            const point = mapToItem(results, pointX, pointY);
            results.itemMenuRequested(index, point.x, point.y, modifiers);
        }
        onDragRequested: paths => results.dragRequested(paths)
        onRenameRequested: (target, name) => results.renameRequested(target, name)
        onSortRequested: key => results.model.setSort(key)
        onZoomRequested: (mode, level) => results.zoomRequested(mode, level)
        onColumnResizeRequested: (id, width) => results.columnResizeRequested(id, width)
        onPreviewKeyRequested: (action, step) => {
            if (action === "toggle") {
                results.previewToggleRequested();
                return;
            }
            drawer.handleKey(action, step);
        }
        onFocusRequested: results.focusRequested()
        onTabRequested: target => results.tabRequested(target)
        onOpenRequested: target => {
            if (target === "..")
                return;
            results.focusRequested();
            results.activateRequested({
                "path": target,
                "isDir": true
            });
        }
    }

    FilePreviewDrawer {
        id: drawer

        anchors.top: viewBody.top
        anchors.bottom: viewBody.bottom
        anchors.right: parent.right
        anchors.rightMargin: FilesMetrics.paneBodyMarginH
        body: viewBody
        open: results.previewOpen
        available: results.width - FilesMetrics.paneBodyMarginH * 2
        preferredWidth: results.previewWidth
        onResized: width => results.previewResized(width)
        onFocusRequested: results.focusRequested()
    }
}
