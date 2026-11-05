import QtQuick
import QtQuick.Effects

Rectangle {
    id: root
    default property alias content: contents.data
    layer.enabled: true
    layer.effect: MultiEffect {
        maskEnabled: true
        maskSource: maskTexture
    }
    data: [
        Item {
            id: contents
            anchors.fill: parent
        },
        Rectangle {
            id: mask
            anchors.fill: parent
            color: "white"
            radius: root.radius
            topLeftRadius: root.topLeftRadius
            topRightRadius: root.topRightRadius
            bottomLeftRadius: root.bottomLeftRadius
            bottomRightRadius: root.bottomRightRadius
        },
        ShaderEffectSource {
            id: maskTexture
            sourceItem: mask
            hideSource: true
            visible: false
            live: true
        }
    ]
}
