pragma ComponentBehavior: Bound

import QtQuick
import Quickshell
import qs.DCommon.Common as DC
import qs.DCommon.Widgets

Item {
    id: root

    property url source: ""
    property real zoom: 1
    property real angle: 0
    readonly property int status: document.status
    readonly property bool ready: status === NativePdfDocument.Ready
    readonly property bool failed: status === NativePdfDocument.Error || renderFailed
    readonly property int pageCount: document.pageCount
    readonly property bool turned: Math.abs(angle % 180) === 90
    readonly property real fitScale: {
        const page = document.pages[0];
        if (!page || width < 2 || height < 2)
            return 1;
        return Math.min(width / (turned ? page.height : page.width), height / (turned ? page.width : page.height));
    }
    readonly property real renderScale: fitScale * zoom
    readonly property real devicePixelRatio: Window.window?.devicePixelRatio ?? Screen.devicePixelRatio
    property bool renderFailed: false

    function applyView() {
        view.returnToBounds();
    }
    function scrollPage(direction) {
        view.contentY = Math.max(view.originY, Math.min(view.maximumContentY, view.contentY + direction * view.height));
    }
    function goToPage(page) {
        if (!ready || pageCount === 0)
            return;
        view.positionViewAtIndex(Math.max(0, Math.min(pageCount - 1, page)), ListView.Beginning);
    }

    onRenderScaleChanged: Qt.callLater(applyView)
    onSourceChanged: {
        renderFailed = false;
        view.contentX = 0;
        view.resetScroll();
    }

    NativePdfDocument {
        id: document
        source: root.source
    }

    DListView {
        id: view
        objectName: "nativePdfPages"
        anchors.fill: parent
        model: document.pages
        spacing: DC.Style.spacingM
        clip: true
        wheelEnabled: false
        focus: false
        activeFocusOnTab: false
        fadeEdges: false
        rowFadeEnabled: false
        reuseItems: true
        contentWidth: document.pages.reduce((widest, page) => Math.max(widest, (root.turned ? page.height : page.width) * root.renderScale), width)
        flickableDirection: Flickable.AutoFlickDirection

        delegate: Item {
            id: page
            required property int index
            required property var modelData
            readonly property real pageWidth: (root.turned ? modelData.height : modelData.width) * root.renderScale
            width: view.contentWidth
            height: (root.turned ? modelData.width : modelData.height) * root.renderScale

            Image {
                anchors.horizontalCenter: parent.horizontalCenter
                width: page.pageWidth
                height: parent.height
                source: document.pageSource(page.index, root.angle)
                sourceSize.width: Math.max(1, Math.ceil(width * root.devicePixelRatio))
                sourceSize.height: Math.max(1, Math.ceil(height * root.devicePixelRatio))
                asynchronous: true
                cache: false
                smooth: true
                onStatusChanged: {
                    if (status === Image.Error)
                        root.renderFailed = true;
                }
            }
        }
    }
}
