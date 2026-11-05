pragma ComponentBehavior: Bound

import QtQuick
import Quickshell
import qs.Common
import qs.Services
import qs.DCommon.FileBrowser

FilePlaceSection {
    id: section

    property string currentPath: ""
    required property string sectionName
    property string openError: ""
    property string busyId: ""
    property var devicesController: null
    readonly property var places: NativePlatform.finderPlaces.filter(place => {
        if (place.section !== sectionName)
            return false;
        if (place.kind === "recent")
            return RecentService.shown;
        const path = place.path || "";
        if (path === "")
            return true;
        if (NetworkService.networkMounts.some(item => item.path === path))
            return SettingsData.showNetworkSection;
        if (NetworkService.deviceMounts.some(item => item.path === path) || DevicesService.devices.some(item => item.mounted && (item.mountPoints?.length ? item.mountPoints : [item.mountPoint]).includes(path)))
            return SettingsData.showDevicesSection;
        return true;
    })

    Component.onCompleted: {
        if (sectionName === "top")
            NativePlatform.refreshFinderPlaces();
    }

    signal navigateRequested(string path)
    signal tabRequested(string path)
    signal dropRequested(string path, var drop)
    signal trashMenuRequested(real x, real y)
    signal recentMenuRequested(real x, real y)

    title: I18n.tr(places[0]?.sectionTitle || "", "native Finder sidebar section heading")
    emptyText: (sectionName === "top" ? NativePlatform.finderPlacesError : "") || openError
    showEmpty: emptyText !== ""
    visible: places.length > 0 || (sectionName === "top" && showEmpty)

    Repeater {
        model: section.places
        FilePlaceRow {
            id: row
            required property var modelData
            readonly property string kind: modelData.kind || "folder"
            readonly property string placePath: kind === "recent" ? RecentService.path : kind === "trash" ? TrashService.filesPath : modelData.path || ""
            readonly property string placeUrl: modelData.url || ""
            readonly property var device: placePath === "" ? null : DevicesService.devices.find(item => item.mounted && (item.mountPoints?.length ? item.mountPoints : [item.mountPoint]).includes(placePath)) ?? null
            readonly property var networkMount: placePath === "" ? null : NetworkService.networkMounts.concat(NetworkService.deviceMounts).find(item => item.path === placePath) ?? null
            label: I18n.tr(modelData.label || "Unavailable location", "native Finder sidebar location name")
            nativeIcon: modelData.icon || ""
            nativeIconColor: selected ? contentColor : section.sectionName === "locations" ? Theme.surfaceText : Theme.primary
            tooltipText: {
                if (!device?.mounted)
                    return "";
                const usage = DevicesService.usage[device.mountPoint];
                const location = [device.mountPoint, device.device].filter(value => value).join("\n");
                if (!usage?.total || usage.code)
                    return location;
                return location + "\n" + I18n.tr("%1 used · %2 free · %3 total", "storage volume usage, free space and total capacity").arg(FileFormat.size(usage.used)).arg(FileFormat.size(usage.free)).arg(FileFormat.size(usage.total));
            }
            iconName: "folder"
            path: kind === "recent" || kind === "trash" ? "" : placePath
            selected: placePath !== "" && placePath === section.currentPath
            dropPath: kind === "recent" ? "" : placePath
            springLoad: placePath !== "" && kind !== "recent" && kind !== "trash"
            actionIcon: networkMount ? "eject" : device && section.devicesController ? section.devicesController.actionIconFor(device) : ""
            actionTooltip: networkMount ? I18n.tr("Disconnect", "network mount action") : device && section.devicesController ? section.devicesController.actionTooltipFor(device) : ""
            busy: (section.busyId !== "" && section.busyId === networkMount?.id) || (device && section.devicesController?.busyId === device.id)
            onClicked: {
                if (placePath !== "") {
                    section.navigateRequested(placePath);
                } else if (placeUrl === "" || !NativePlatform.openFinderPlace(placeUrl)) {
                    section.openError = I18n.tr("This Finder location cannot be opened", "Finder shared location open failure");
                } else {
                    section.openError = "";
                }
            }
            onMiddleClicked: {
                if (placePath !== "")
                    section.tabRequested(placePath);
            }
            onDropped: drop => section.dropRequested(placePath, drop)
            onSpringLoaded: section.navigateRequested(placePath)
            onActionTriggered: {
                if (section.busyId !== "")
                    return;
                if (networkMount) {
                    section.busyId = networkMount.id;
                    NetworkService.disconnect(networkMount.id, () => section.busyId = "");
                } else if (device && section.devicesController) {
                    section.devicesController.detach(device);
                }
            }

            TapHandler {
                acceptedButtons: Qt.RightButton
                onTapped: point => {
                    const mapped = row.mapToItem(section, point.position.x, point.position.y);
                    switch (row.kind) {
                    case "recent":
                        section.recentMenuRequested(mapped.x, mapped.y);
                        break;
                    case "trash":
                        section.trashMenuRequested(mapped.x, mapped.y);
                        break;
                    }
                }
            }
        }
    }
}
