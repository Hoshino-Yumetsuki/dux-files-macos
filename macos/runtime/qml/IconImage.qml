import QtQuick

Item {
    id: root
    property real implicitSize: 0
    property alias source: image.source
    property alias status: image.status
    property alias asynchronous: image.asynchronous
    property alias mipmap: image.mipmap
    property alias fillMode: image.fillMode
    readonly property alias backer: image
    implicitWidth: implicitSize > 0 ? implicitSize : image.implicitWidth
    implicitHeight: implicitSize > 0 ? implicitSize : image.implicitHeight
    Image {
        id: image
        anchors.fill: parent
        fillMode: Image.PreserveAspectFit
        smooth: root.smooth
    }
}
