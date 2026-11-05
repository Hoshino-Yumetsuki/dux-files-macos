#include "../../../../macos/native/volumes.mm"
#include <cassert>
#include <QTemporaryDir>

void publishEvent(const QString &, const QJsonValue &) {}

int main() {
    @autoreleasepool {
        assert(physicalStoreID(@{@"APFSPhysicalStore": @"disk0s2"}) == "disk0s2");
        assert(physicalStoreID(@{@"DeviceIdentifier": @"disk0s2"}) == "disk0s2");
        assert(wholeDiskID(@{@"WholeDisk": @YES, @"DeviceIdentifier": @"disk0"}) == "disk0");
        assert(wholeDiskID(@{@"WholeDisk": @NO, @"DeviceIdentifier": @"disk0s2", @"ParentWholeDisk": @"disk0"}) == "disk0");
        assert(wholeDiskID(@{@"DeviceIdentifier": @"disk3s1s1", @"ParentWholeDisk": @"disk3"}) == "disk3");
        for (NSDictionary *info in @[@{}, @{@"ParentWholeDisk": @"disk0s2"}, @{@"ParentWholeDisk": @"/dev/disk0"},
                 @{@"WholeDisk": @YES, @"DeviceIdentifier": @"disk3s1"}]) {
            bool rejected = false;
            try {
                wholeDiskID(info);
            } catch (const std::runtime_error &error) {
                rejected = QString::fromUtf8(error.what()).startsWith("EIO: ");
            }
            assert(rejected);
        }
        QTemporaryDir plugins;
        assert(plugins.isValid());
        assert(installedNetworkSchemes({plugins.path()}).isEmpty());
        assert(QDir(plugins.path()).mkpath("smb.bundle"));
        assert(QDir(plugins.path()).mkpath("http.bundle"));
        assert(QDir(plugins.path()).mkpath("unknown.bundle"));
        assert(installedNetworkSchemes({plugins.path(), plugins.path()}) == QStringList({"smb", "dav", "davs"}));
        assert(QDir(plugins.path()).mkpath("afp.bundle"));
        assert(QDir(plugins.path()).mkpath("nfs.bundle"));
        assert(QDir(plugins.path()).mkpath("ftp.bundle"));
        assert(installedNetworkSchemes({plugins.path()}) == QStringList({"smb", "afp", "nfs", "dav", "davs", "ftp"}));
        for (const auto &method : {"volumes.unknown", "network.unknown"}) {
            bool rejected = false;
            try {
                volumes(QString::fromLatin1(method), {});
            } catch (const std::runtime_error &error) {
                rejected = QString::fromUtf8(error.what()).startsWith("NOTSUPPORTED: ");
            }
            assert(rejected);
        }
        QMap<QString, QString> containers{{"disk3s1", "disk3"}, {"disk3s11", "disk3"}, {"disk7s1", "disk7"}};
        assert(rootVolumeID("disk3s1s1", {}, containers) == "disk3s1");
        assert(rootVolumeID("disk3s11s1", {}, containers) == "disk3s11");
        assert(rootVolumeID("disk3s1", {}, containers) == "disk3s1");
        assert(rootVolumeID("snapshot", {{"snapshot", "disk3s1"}}, containers) == "disk3s1");
        assert(rootVolumeID("disk30s1", {}, containers).isEmpty());

        QList<Volume> volumes{
            {QJsonObject{{"protected", true}}, {"disk0"}},
            {QJsonObject{{"protected", false}}, {"disk1"}},
            {QJsonObject{{"protected", false}}, {"disk0", "disk1"}},
            {QJsonObject{{"protected", false}}, {"disk8"}}
        };
        protectDrives(volumes, {});
        assert(volumes[1].json["protected"].toBool());
        assert(volumes[2].json["protected"].toBool());
        assert(!volumes[3].json["protected"].toBool());

        auto share = networkMount("/Volumes/Photos", QUrl("smb://guest:secret@server/Photos"));
        assert(!share["uri"].toString().contains("secret"));
        assert(locationIn(share, QUrl("smb://guest@server/Photos/Trip%20One")) == "/Volumes/Photos/Trip One");
        assert(locationIn(share, QUrl("smb://guest@server/Photos2")).isEmpty());
        assert(locationIn(share, QUrl("smb://guest@other/Photos")).isEmpty());
        assert(locationIn(share, QUrl("smb://other@server/Photos")).isEmpty());
        for (const QString &path : {"../private", "%2e%2e/private", "%00"}) {
            bool rejected = false;
            try {
                locationIn(share, QUrl("smb://guest@server/Photos/" + path));
            } catch (const std::runtime_error &error) {
                rejected = QString::fromUtf8(error.what()).startsWith("EINVAL: ");
            }
            assert(rejected);
        }
        assert(networkMount("/Volumes/Web", QUrl("https://host/dir"))["scheme"] == "davs");
        assert(protectedPath("/System/Volumes/Data"));
        assert(protectedPath("/Users/example"));
        assert(!protectedPath("/Volumes/Photos"));
    }
}
