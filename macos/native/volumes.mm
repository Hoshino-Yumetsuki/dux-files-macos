#include "native.h"

#import <AppKit/AppKit.h>
#import <DiskArbitration/DiskArbitration.h>
#import <NetFS/NetFS.h>
#include <QCoreApplication>
#include <QDir>
#include <QFile>
#include <QRegularExpression>
#include <QTimer>
#include <QJsonArray>
#include <QMap>
#include <QSet>
#include <QUrl>
#include <algorithm>
#include <stdexcept>
#include <vector>
#include <sys/mount.h>

namespace {
QString text(NSString *value) {
    return value ? QString::fromUtf8(value.UTF8String) : QString();
}

NSString *string(const QString &value) {
    return [NSString stringWithUTF8String:value.toUtf8().constData()];
}

[[noreturn]] void fail(const char *code, const QString &message) {
    throw std::runtime_error((QString::fromLatin1(code) + ": " + message).toStdString());
}

void checkStatus(int status, const QString &operation) {
    if (status == 0) return;
    const char *code = "EIO";
    switch (status) {
    case EPERM: case kDAReturnNotPermitted: code = "EPERM"; break;
    case EACCES: case kDAReturnNotPrivileged: case -5000: code = "EACCES"; break;
    case EBUSY: case kDAReturnBusy: code = "EBUSY"; break;
    case ENOENT: case kDAReturnNotFound: case kDAReturnNotReady: code = "ENOENT"; break;
    case EINVAL: case kDAReturnBadArgument: code = "EINVAL"; break;
    case ENOTSUP: case EPROTONOSUPPORT: case kDAReturnUnsupported:
    case ENETFSNOAUTHMECHSUPP: case ENETFSNOPROTOVERSSUPP: code = "NOTSUPPORTED"; break;
    case ECANCELED: case -128: code = "ECANCELED"; break;
    }
    fail(code, operation + " failed (" + QString::number(status) + ")");
}

NSData *diskutil(NSArray<NSString *> *args, NSData *input = nil) {
    NSTask *task = [NSTask new];
    task.executableURL = [NSURL fileURLWithPath:@"/usr/sbin/diskutil"];
    task.arguments = args;
    NSPipe *output = [NSPipe pipe];
    task.standardOutput = output;
    task.standardError = [NSFileHandle fileHandleWithNullDevice];
    NSPipe *in = input ? [NSPipe pipe] : nil;
    task.standardInput = in ?: [NSFileHandle fileHandleWithNullDevice];
    NSError *error = nil;
    if (![task launchAndReturnError:&error]) fail("EIO", "could not start diskutil");
    if (input) {
        [in.fileHandleForWriting writeData:input];
        [in.fileHandleForWriting closeFile];
    }
    NSData *data = [output.fileHandleForReading readDataToEndOfFile];
    [task waitUntilExit];
    if (task.terminationStatus != 0) {
        fail(input ? "EACCES" : "EIO", input ? "APFS authentication failed" : "diskutil could not read or update the volume");
    }
    return data;
}

NSDictionary *diskPlist(NSArray<NSString *> *args) {
    NSData *data = diskutil(args);
    id plist = [NSPropertyListSerialization propertyListWithData:data options:NSPropertyListImmutable format:nil error:nil];
    if (![plist isKindOfClass:[NSDictionary class]]) fail("EIO", "invalid diskutil response");
    return plist;
}

struct Session {
    DASessionRef value = DASessionCreate(kCFAllocatorDefault);
    Session() { if (!value) fail("EIO", "Disk Arbitration is unavailable"); }
    ~Session() { CFRelease(value); }
};

struct Disk {
    DADiskRef value;
    Disk(DASessionRef session, const QString &id)
        : value(DADiskCreateFromBSDName(kCFAllocatorDefault, session, id.toUtf8().constData())) {
        if (!value) fail("ENOENT", "unknown disk");
    }
    ~Disk() { CFRelease(value); }
};

QString wholeDiskID(NSDictionary *info) {
    QString id = [info[@"WholeDisk"] boolValue] ? text(info[@"DeviceIdentifier"]) : text(info[@"ParentWholeDisk"]);
    static const QRegularExpression wholeName("^disk[0-9]+$");
    if (!wholeName.match(id).hasMatch()) fail("EIO", "cannot determine the whole disk");
    return id;
}

QString wholeDisk(DASessionRef session, const QString &id) {
    Disk disk(session, id);
    DADiskRef whole = DADiskCopyWholeDisk(disk.value);
    if (whole) {
        QString result = QString::fromUtf8(DADiskGetBSDName(whole));
        CFRelease(whole);
        if (result.isEmpty()) fail("EIO", "whole disk has no device identifier");
        return result;
    }
    QString parent = wholeDiskID(diskPlist(@[@"info", @"-plist", string(id)]));
    NSDictionary *info = diskPlist(@[@"info", @"-plist", string(parent)]);
    if (![info[@"WholeDisk"] boolValue] || text(info[@"DeviceIdentifier"]) != parent) {
        fail("EIO", "whole disk identity could not be verified");
    }
    return parent;
}

bool protectedPath(const QString &path) {
    return path == "/" || path.startsWith("/System/") || path == "/System"
        || path == "/private" || path.startsWith("/private/")
        || path == "/Users" || path.startsWith("/Users/") || path.startsWith("/dev/");
}

struct Volume {
    QJsonObject json;
    QSet<QString> drives;
};

QString physicalStoreID(NSDictionary *store) {
    QString id = text(store[@"DeviceIdentifier"]);
    if (id.isEmpty()) id = text(store[@"APFSPhysicalStore"]);
    if (id.isEmpty()) fail("EIO", "APFS physical store has no device identifier");
    return id;
}

QString rootVolumeID(const QString &id, const QMap<QString, QString> &snapshots, const QMap<QString, QString> &containers) {
    QString root = snapshots.value(id, id);
    if (containers.contains(root)) return root;
    root.clear();
    for (auto it = containers.cbegin(); it != containers.cend(); ++it) {
        if (id.startsWith(it.key() + "s") && it.key().size() > root.size()) root = it.key();
    }
    return root;
}

void protectDrives(QList<Volume> &volumes, QSet<QString> protectedDrives) {
    qsizetype previous;
    // ponytail: rescan small disk graphs; use adjacency traversal if inventories grow.
    do {
        previous = protectedDrives.size();
        for (const Volume &volume : volumes) {
            if (!volume.json["protected"].toBool() && !volume.drives.intersects(protectedDrives)) continue;
            protectedDrives.unite(volume.drives);
        }
    } while (protectedDrives.size() != previous);
    for (Volume &volume : volumes) {
        if (volume.drives.intersects(protectedDrives)) volume.json["protected"] = true;
    }
}

QList<Volume> localVolumes(DASessionRef session) {
    NSDictionary *listing = diskPlist(@[@"list", @"-plist"]);
    NSDictionary *apfs = diskPlist(@[@"apfs", @"list", @"-plist"]);
    QMap<QString, NSDictionary *> crypto;
    QMap<QString, QSet<QString>> stores;
    QMap<QString, QString> containers;
    QMap<QString, QString> snapshots;
    for (NSDictionary *disk in listing[@"AllDisksAndPartitions"]) {
        for (NSDictionary *volume in disk[@"APFSVolumes"]) {
            for (NSDictionary *snapshot in volume[@"MountedSnapshots"]) {
                snapshots[text(snapshot[@"SnapshotBSD"])] = text(volume[@"DeviceIdentifier"]);
            }
        }
    }
    for (NSDictionary *container in apfs[@"Containers"]) {
        QString ref = text(container[@"ContainerReference"]);
        for (NSDictionary *store in container[@"PhysicalStores"]) {
            stores[ref].insert(wholeDisk(session, physicalStoreID(store)));
        }
        for (NSDictionary *volume in container[@"Volumes"]) {
            QString id = text(volume[@"DeviceIdentifier"]);
            crypto[id] = volume;
            containers[id] = ref;
        }
    }
    QList<Volume> volumes;
    for (NSString *identifier in listing[@"AllDisks"]) {
        QString id = text(identifier);
        if (containers.contains(id) || stores.contains(id) || snapshots.contains(id)) continue;
        Disk disk(session, id);
        CFDictionaryRef description = DADiskCopyDescription(disk.value);
        if (!description) continue;
        NSDictionary *desc = CFBridgingRelease(description);
        NSString *kind = desc[(__bridge NSString *)kDADiskDescriptionVolumeKindKey];
        if (!kind) continue;
        containers[id] = QString();
    }

    QSet<QString> protectedDrives;
    NSDictionary *root = diskPlist(@[@"info", @"-plist", @"/"]);
    QString rootID = text(root[@"APFSVolumeDisk"]);
    if (rootID.isEmpty()) rootID = text(root[@"DeviceIdentifier"]);
    if (rootID.isEmpty()) fail("EIO", "cannot identify the system volume");
    QString rootVolume = rootVolumeID(rootID, snapshots, containers);
    protectedDrives.unite(stores.value(containers.value(rootVolume)));
    for (NSDictionary *store in root[@"APFSPhysicalStores"]) {
        protectedDrives.insert(wholeDisk(session, physicalStoreID(store)));
    }
    protectedDrives.insert(wholeDisk(session, rootVolume.isEmpty() ? rootID : rootVolume));
    if (text(root[@"FilesystemType"]) == "apfs" && stores.value(containers.value(rootVolume)).isEmpty()) {
        fail("EIO", "cannot identify all system APFS physical stores");
    }
    for (auto it = containers.cbegin(); it != containers.cend(); ++it) {
        QString id = it.key();
        NSDictionary *info = diskPlist(@[@"info", @"-plist", string(id)]);
        QString point = text(info[@"MountPoint"]);
        for (auto snap = snapshots.cbegin(); snap != snapshots.cend(); ++snap) {
            if (snap.value() != id) continue;
            NSDictionary *snapshot = diskPlist(@[@"info", @"-plist", string(snap.key())]);
            if (text(snapshot[@"MountPoint"]) == "/") point = "/";
        }
        QSet<QString> drives = stores.value(it.value());
        if (drives.isEmpty()) drives.insert(wholeDisk(session, id));
        NSDictionary *encryption = crypto.value(id);
        bool encrypted = [encryption[@"Encryption"] boolValue] || [encryption[@"FileVault"] boolValue];
        bool unlocked = encrypted && ![encryption[@"Locked"] boolValue];
        if (id == rootVolume) point = "/";
        bool system = point == "/";
        bool protect = protectedPath(point);
        if (protect) protectedDrives.unite(drives);
        bool internal = [info[@"Internal"] boolValue];
        bool removable = [info[@"Removable"] boolValue] || [info[@"RemovableMedia"] boolValue] || !internal;
        bool ejectable = [info[@"Ejectable"] boolValue] || !internal;
        QString name = text(info[@"VolumeName"]);
        if (name.isEmpty()) name = text(encryption[@"Name"]);
        if (name.isEmpty()) name = text(info[@"MediaName"]);
        if (name.isEmpty()) name = id;
        bool hidden = [info[@"OSInternalMedia"] boolValue];
        for (NSString *role in encryption[@"Roles"]) {
            if (![role isEqualToString:@"Data"] && ![role isEqualToString:@"System"]) hidden = true;
        }
        if (protect && !system) hidden = true;
        QString kind = system ? "system" : removable ? "removable" : "internal";
        QString filesystem = text(info[@"FilesystemType"]);
        if (filesystem.isEmpty() && encryption) filesystem = "apfs";
        QJsonObject json{
            {"id", id}, {"kind", kind}, {"name", name}, {"label", name},
            {"device", "/dev/" + id}, {"drive", *drives.cbegin()},
            {"fstype", filesystem}, {"uuid", text(info[@"VolumeUUID"])},
            {"size", static_cast<qint64>([info[@"TotalSize"] unsignedLongLongValue])},
            {"removable", removable}, {"ejectable", ejectable}, {"canPowerOff", false},
            {"encrypted", encrypted}, {"unlocked", unlocked}, {"cleartext", unlocked ? id : QString()},
            {"mounted", !point.isEmpty()}, {"mountPoint", point},
            {"mountPoints", point.isEmpty() ? QJsonArray{} : QJsonArray{point}},
            {"iconName", system ? "drive-harddisk-system" : removable ? "drive-removable-media" : "drive-harddisk"},
            {"system", system}, {"protected", protect}, {"hidden", hidden}
        };
        volumes.append({json, drives});
    }
    protectDrives(volumes, protectedDrives);
    std::sort(volumes.begin(), volumes.end(), [](const Volume &a, const Volume &b) {
        if (a.json["system"].toBool() != b.json["system"].toBool()) return a.json["system"].toBool();
        return a.json["name"].toString().compare(b.json["name"].toString(), Qt::CaseInsensitive) < 0;
    });
    return volumes;
}

struct Completion {
    dispatch_semaphore_t done = dispatch_semaphore_create(0);
    int status = 0;
};

void completed(DADiskRef, DADissenterRef dissenter, void *context) {
    auto *completion = static_cast<Completion *>(context);
    if (dissenter) completion->status = DADissenterGetStatus(dissenter);
    dispatch_semaphore_signal(completion->done);
}

void diskAction(DASessionRef session, DADiskRef disk, const QString &method, bool force = false) {
    dispatch_queue_t queue = dispatch_queue_create("dev.duxlabs.files.disk-action", DISPATCH_QUEUE_SERIAL);
    DASessionSetDispatchQueue(session, queue);
    Completion completion;
    if (method == "mount") {
        DADiskMount(disk, nullptr, kDADiskMountOptionDefault, completed, &completion);
    } else if (method == "eject") {
        DADiskEject(disk, kDADiskEjectOptionDefault, completed, &completion);
    } else {
        DADiskUnmountOptions options = force ? kDADiskUnmountOptionForce : kDADiskUnmountOptionDefault;
        if (method == "unmountWhole") options |= kDADiskUnmountOptionWhole;
        DADiskUnmount(disk, options, completed, &completion);
    }
    dispatch_semaphore_wait(completion.done, DISPATCH_TIME_FOREVER);
    DASessionSetDispatchQueue(session, nullptr);
    checkStatus(completion.status, method);
}

QJsonValue volumeAction(const QString &method, const QJsonObject &params) {
    const QSet<QString> methods{"volumes.list", "volumes.mount", "volumes.unmount", "volumes.eject",
        "volumes.powerOff", "volumes.unlock", "volumes.lock"};
    if (!methods.contains(method)) fail("NOTSUPPORTED", "unknown volume operation");
    if (method != "volumes.list" && params["id"].toString().isEmpty()) fail("EINVAL", "volume id is required");
    Session session;
    QList<Volume> all = localVolumes(session.value);
    if (method == "volumes.list") {
        QJsonArray devices;
        for (const auto &volume : all) devices.append(volume.json);
        return QJsonObject{{"available", true}, {"devices", devices}};
    }
    QString id = params["id"].toString();
    auto found = std::find_if(all.cbegin(), all.cend(), [&](const Volume &v) { return v.json["id"] == id; });
    if (found == all.cend()) fail("ENOENT", "unknown volume");
    const QJsonObject &volume = found->json;
    QString action = method.mid(8);
    if (action != "mount" && action != "unlock" && volume["protected"].toBool()) {
        fail("EPERM", "volume belongs to a protected system disk");
    }
    Disk disk(session.value, id);
    if (action == "mount") {
        if (volume["encrypted"].toBool() && !volume["unlocked"].toBool()) fail("EINVAL", "APFS volume is locked");
        if (!volume["mounted"].toBool()) diskAction(session.value, disk.value, action);
        NSDictionary *info = diskPlist(@[@"info", @"-plist", string(id)]);
        QString point = text(info[@"MountPoint"]);
        if (point.isEmpty()) fail("EIO", "mounted volume has no mount point");
        return QJsonObject{{"mountPoint", point}};
    }
    if (action == "unmount") {
        if (volume["mounted"].toBool()) diskAction(session.value, disk.value, action, params["force"].toBool());
        return QJsonObject{};
    }
    if (action == "powerOff") fail("NOTSUPPORTED", "macOS supports safe ejection, not device power-off");
    if (action == "eject") {
        if (!volume["ejectable"].toBool()) fail("NOTSUPPORTED", "disk cannot be ejected");
        for (const QString &drive : found->drives) {
            Disk whole(session.value, drive);
            diskAction(session.value, whole.value, "unmountWhole");
        }
        for (const QString &drive : found->drives) {
            Disk whole(session.value, drive);
            diskAction(session.value, whole.value, "eject");
        }
        return QJsonObject{};
    }
    if (action == "lock" || action == "unlock") {
        if (!volume["encrypted"].toBool() || volume["fstype"] != "apfs") fail("EINVAL", "volume is not encrypted APFS");
        if (action == "lock") {
            if (volume["unlocked"].toBool()) diskutil(@[@"apfs", @"lockVolume", string(id)]);
            return QJsonObject{};
        }
        if (!volume["unlocked"].toBool()) {
            QString passphrase = params["passphrase"].toString();
            if (passphrase.isEmpty() || passphrase.contains('\n') || passphrase.contains('\r') || passphrase.contains(QChar::Null)) {
                fail("EINVAL", "invalid APFS passphrase");
            }
            QByteArray input = passphrase.toUtf8() + '\n';
            diskutil(@[@"apfs", @"unlockVolume", string(id), @"-stdinpassphrase", @"-nomount"],
                [NSData dataWithBytes:input.constData() length:input.size()]);
            input.fill(0);
        }
        return QJsonObject{{"cleartext", id}};
    }
    fail("NOTSUPPORTED", "unknown volume operation");
}

QStringList installedNetworkSchemes(const QStringList &roots = {
    "/System/Library/Filesystems/NetFSPlugins", "/Library/Filesystems/NetFSPlugins"}) {
    QStringList schemes;
    const QList<QPair<QString, QStringList>> plugins{
        {"smb", {"smb"}}, {"afp", {"afp"}}, {"nfs", {"nfs"}},
        {"http", {"dav", "davs"}}, {"ftp", {"ftp"}}
    };
    for (const auto &plugin : plugins) {
        for (const QString &root : roots) {
            if (!QDir(root + '/' + plugin.first + ".bundle").exists()) continue;
            schemes.append(plugin.second);
            break;
        }
    }
    return schemes;
}

QUrl publicURL(QUrl url) {
    url.setPassword(QString());
    url.setQuery(QString());
    url.setFragment(QString());
    if (url.scheme() == "http") url.setScheme("dav");
    if (url.scheme() == "https") url.setScheme("davs");
    return url;
}

QJsonObject networkMount(const QString &path, QUrl uri) {
    uri = publicURL(uri);
    return {{"id", path}, {"name", QDir(path).dirName()}, {"uri", uri.toString(QUrl::FullyEncoded)},
        {"path", path}, {"scheme", uri.scheme()}, {"kind", "network"}, {"iconName", "folder-remote"}};
}

QJsonArray networkMounts() {
    int count = getfsstat(nullptr, 0, MNT_NOWAIT);
    if (count < 0) checkStatus(errno, "list mounts");
    std::vector<struct statfs> mounts(static_cast<size_t>(count) + 16);
    count = getfsstat(mounts.data(), static_cast<int>(mounts.size() * sizeof(struct statfs)), MNT_NOWAIT);
    if (count < 0) checkStatus(errno, "list mounts");
    QJsonArray result;
    for (int i = 0; i < count; ++i) {
        const auto &mount = mounts[i];
        if (mount.f_flags & (MNT_LOCAL | MNT_DONTBROWSE)) continue;
        QString path = QFile::decodeName(mount.f_mntonname);
        NSURL *local = [NSURL fileURLWithPath:string(path) isDirectory:YES];
        NSURL *remote = CFBridgingRelease(NetFSCopyURLForRemountingVolume((__bridge CFURLRef)local));
        QUrl uri(text(remote.absoluteString));
        if (uri.isEmpty()) {
            QString source = QString::fromUtf8(mount.f_mntfromname);
            QString type = QString::fromUtf8(mount.f_fstypename);
            if (type == "smbfs" && source.startsWith("//")) uri = QUrl("smb:" + source);
            if (type == "webdav") uri = QUrl(source);
            if (type == "nfs") {
                const auto separator = source.indexOf(":/");
                if (separator >= 0) uri = QUrl("nfs://" + source.left(separator) + source.mid(separator + 1));
            }
        }
        if (!uri.isValid() || uri.host().isEmpty()) continue;
        result.append(networkMount(path, uri));
    }
    return result;
}

QString locationIn(const QJsonObject &mount, const QUrl &requested) {
    QUrl root(mount["uri"].toString());
    if (root.scheme() != requested.scheme() || root.host() != requested.host() || root.port() != requested.port()) return {};
    if (!requested.userName().isEmpty() && root.userName() != requested.userName()) return {};
    QString base = root.path();
    while (base.endsWith('/')) base.chop(1);
    QString path = requested.path();
    if (path != base && !path.startsWith(base + '/')) return {};
    QString relative = path.mid(base.size());
    for (const QString &part : relative.split('/')) {
        if (part == ".." || part == "." || part.contains(QChar::Null)) fail("EINVAL", "invalid network path");
    }
    return mount["path"].toString() + relative;
}

QJsonValue networkAction(const QString &method, const QJsonObject &params) {
    if (method == "network.list") {
        return QJsonObject{{"kind", "mounts"}, {"available", true},
            {"schemes", QJsonArray::fromStringList(installedNetworkSchemes())}, {"mounts", networkMounts()}};
    }
    if (method == "network.disconnect") {
        QString id = params["id"].toString();
        if (id.isEmpty()) fail("EINVAL", "network mount id is required");
        for (const auto &value : networkMounts()) {
            QJsonObject mount = value.toObject();
            if (mount["id"] != id && mount["uri"] != id && mount["path"] != id) continue;
            QString path = mount["path"].toString();
            if (protectedPath(path)) fail("EPERM", "cannot disconnect a system mount");
            if (unmount(QFile::encodeName(path).constData(), 0) != 0) checkStatus(errno, "disconnect");
            return QJsonObject{};
        }
        fail("ENOENT", "unknown network mount");
    }
    if (method != "network.connect") fail("NOTSUPPORTED", "unknown network operation");
    QUrl uri(params["uri"].toString(), QUrl::StrictMode);
    if (!uri.isValid() || uri.host().isEmpty() || uri.hasQuery() || uri.hasFragment()) fail("EINVAL", "invalid network address");
    if (!installedNetworkSchemes().contains(uri.scheme())) fail("NOTSUPPORTED", "no native backend handles this network scheme");
    for (const QString &part : uri.path().split('/')) {
        if (part == ".." || part == "." || part.contains(QChar::Null)) fail("EINVAL", "invalid network path");
    }
    QString password = params["password"].toString();
    if (password.isEmpty()) password = uri.password();
    uri = publicURL(uri);
    for (const auto &value : networkMounts()) {
        QJsonObject mount = value.toObject();
        QString location = locationIn(mount, uri);
        if (!location.isEmpty()) return QJsonObject{{"mount", mount}, {"location", location}, {"shares", QJsonArray{}}};
    }
    QUrl nativeURI = uri;
    if (nativeURI.scheme() == "dav") nativeURI.setScheme("http");
    if (nativeURI.scheme() == "davs") nativeURI.setScheme("https");
    NSURL *url = [NSURL URLWithString:string(nativeURI.toString(QUrl::FullyEncoded))];
    if (!url) fail("EINVAL", "invalid network address");
    CFArrayRef mounted = nullptr;
    int status = NetFSMountURLSync((__bridge CFURLRef)url, nullptr,
        uri.userName().isEmpty() ? nullptr : (__bridge CFStringRef)string(uri.userName()),
        password.isEmpty() ? nullptr : (__bridge CFStringRef)string(password), nullptr, nullptr, &mounted);
    NSArray *points = CFBridgingRelease(mounted);
    checkStatus(status, "network connection");
    if (points.count == 0) fail("EIO", "network connection returned no mount point");
    QString point = text(points.firstObject);
    for (const auto &value : networkMounts()) {
        QJsonObject mount = value.toObject();
        if (mount["path"] != point) continue;
        QString location = locationIn(mount, uri);
        if (location.isEmpty()) location = point;
        return QJsonObject{{"mount", mount}, {"location", location}, {"shares", QJsonArray{}}};
    }
    fail("EIO", "connected network mount could not be resolved");
}

void diskChanged(DADiskRef, void *context) {
    auto *timer = static_cast<QTimer *>(context);
    if (!timer->isActive()) timer->start();
}

void diskDescriptionChanged(DADiskRef disk, CFArrayRef, void *context) {
    diskChanged(disk, context);
}
}

QJsonValue volumes(const QString &method, const QJsonObject &params) {
    if (method.startsWith("volumes.")) return volumeAction(method, params);
    if (method.startsWith("network.")) return networkAction(method, params);
    fail("NOTSUPPORTED", "unknown volume or network operation");
}

void watchVolumes() {
    static bool watching = false;
    if (watching) return;
    static Session session;
    auto *timer = new QTimer(QCoreApplication::instance());
    timer->setSingleShot(true);
    timer->setInterval(150);
    QObject::connect(timer, &QTimer::timeout, [] { publishEvent("volumes"); });
    DARegisterDiskAppearedCallback(session.value, nullptr, diskChanged, timer);
    DARegisterDiskDisappearedCallback(session.value, nullptr, diskChanged, timer);
    DARegisterDiskDescriptionChangedCallback(session.value, nullptr, nullptr, diskDescriptionChanged, timer);
    DASessionScheduleWithRunLoop(session.value, CFRunLoopGetMain(), kCFRunLoopCommonModes);
    NSNotificationCenter *center = NSWorkspace.sharedWorkspace.notificationCenter;
    for (NSString *name in @[NSWorkspaceDidMountNotification, NSWorkspaceDidUnmountNotification, NSWorkspaceDidRenameVolumeNotification]) {
        [center addObserverForName:name object:nil queue:NSOperationQueue.mainQueue usingBlock:^(NSNotification *) {
            if (!timer->isActive()) timer->start();
        }];
    }
    watching = true;
}
