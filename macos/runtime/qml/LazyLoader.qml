import QtQuick

Loader {
    default property alias component: loader.sourceComponent
    id: loader
    asynchronous: false
}
