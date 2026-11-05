#include "native.h"

#import <Foundation/Foundation.h>
#include <QCryptographicHash>
#include <QDir>
#include <QFile>
#include <QFileInfo>
#include <QUuid>
#include <QJsonArray>
#include <QJsonDocument>
#include <QLockFile>
#include <QSaveFile>
#include <QSet>
#include <algorithm>
#include <cerrno>
#include <fcntl.h>
#include <cstring>
#include <cstdio>
#include <stdexcept>
#include <sys/stat.h>
#include <unistd.h>

namespace {
QString text(NSString *value) { return value ? QString::fromUtf8(value.UTF8String) : QString(); }
NSString *string(const QString &value) { return [NSString stringWithUTF8String:value.toUtf8().constData()]; }

[[noreturn]] void fail(const QString &code, const QString &message) {
    throw std::runtime_error((code + ": " + message).toStdString());
}

[[noreturn]] void posixError(const QString &path) {
    const int error = errno;
    QString code = QStringLiteral("EIO");
    switch (error) {
    case EEXIST: case ENOTEMPTY: code = QStringLiteral("EXISTS"); break;
    case ENOENT: code = QStringLiteral("NOTFOUND"); break;
    case EACCES: case EPERM: code = QStringLiteral("ACCESS"); break;
    }
    fail(code, path + ": " + QString::fromLocal8Bit(strerror(error)));
}

QString absolutePath(const QString &path) {
    if (!path.startsWith('/') || path.contains(QChar::Null)) fail("INVALID", "expected an absolute local path");
    const auto cleaned = QDir::cleanPath(path);
    if (cleaned == "/") fail("INVALID", "the filesystem root is not a trash item");
    return cleaned;
}

struct stat status(const QString &path) {
    struct stat result{};
    if (lstat(QFile::encodeName(path).constData(), &result) != 0) posixError(path);
    return result;
}

bool present(const QString &path) {
    struct stat result{};
    if (lstat(QFile::encodeName(path).constData(), &result) == 0) return true;
    if (errno == ENOENT) return false;
    posixError(path);
}

QString metadataDirectory() {
    NSError *error = nil;
    NSURL *support = [NSFileManager.defaultManager URLForDirectory:NSApplicationSupportDirectory inDomain:NSUserDomainMask appropriateForURL:nil create:YES error:&error];
    if (!support) fail("EIO", text(error.localizedDescription));
    const auto directory = text([support URLByAppendingPathComponent:@"Dux Files/Trash" isDirectory:YES].path);
    if (!QDir().mkpath(directory)) fail("ACCESS", "cannot create trash metadata directory");
    const auto st = status(directory);
    if (!S_ISDIR(st.st_mode) || st.st_uid != getuid()) fail("ACCESS", "unsafe trash metadata directory");
    if (chmod(QFile::encodeName(directory).constData(), 0700) != 0) posixError(directory);
    return directory;
}

QString metadataPath(const QString &directory, const QString &path) {
    return directory + '/' + QString::fromLatin1(QCryptographicHash::hash(path.toUtf8(), QCryptographicHash::Sha256).toHex()) + ".json";
}

QString identity(const struct stat &st) {
    return QString::number(st.st_dev) + ':' + QString::number(st.st_ino) + ':' +
           QString::number(st.st_birthtimespec.tv_sec) + ':' + QString::number(st.st_birthtimespec.tv_nsec);
}

QJsonObject readSaved(const QString &filePath) {
    const int fd = open(QFile::encodeName(filePath).constData(), O_RDONLY | O_NOFOLLOW | O_NONBLOCK);
    if (fd < 0) return {};
    struct stat info{};
    if (fstat(fd, &info) != 0 || !S_ISREG(info.st_mode) || info.st_uid != getuid() ||
        (info.st_mode & 0077) != 0 || info.st_size > 65536) {
        close(fd);
        return {};
    }
    QFile file;
    if (!file.open(fd, QIODevice::ReadOnly, QFileDevice::AutoCloseHandle)) {
        close(fd);
        return {};
    }
    const auto object = QJsonDocument::fromJson(file.read(65537)).object();
    const auto origin = object.value("originalPath").toString();
    if (!origin.startsWith('/') || origin == "/" || origin.contains(QChar::Null)) return {};
    return object;
}

QJsonObject readMetadata(const QString &filePath, const QString &path, const struct stat &st) {
    auto object = readSaved(filePath);
    const auto recordedPath = object.value("filesPath").toString();
    if ((recordedPath.isEmpty() && !QFileInfo(filePath).fileName().startsWith("pending-")) ||
        (!recordedPath.isEmpty() && recordedPath != path) || object.value("identity").toString() != identity(st)) return {};
    object.insert("infoPath", filePath);
    return object;
}

QJsonObject metadata(const QString &directory, const QString &path, const struct stat &st) {
    auto object = readMetadata(metadataPath(directory, path), path, st);
    if (!object.isEmpty()) return object;
    for (const auto &name : QDir(directory).entryList({"pending-*.json"}, QDir::Files | QDir::NoSymLinks)) {
        object = readMetadata(directory + '/' + name, path, st);
        if (!object.isEmpty()) return object;
    }
    return {};
}

void writeMetadata(const QString &directory, const QString &path, const QJsonObject &object) {
    QSaveFile file(metadataPath(directory, path));
    if (!file.open(QIODevice::WriteOnly)) fail("EIO", file.errorString());
    file.setPermissions(QFileDevice::ReadOwner | QFileDevice::WriteOwner);
    const auto body = QJsonDocument(object).toJson(QJsonDocument::Compact);
    if (file.write(body) != body.size() || !file.commit()) fail("EIO", file.errorString());
}

QSet<QString> trashDirectories() {
    QSet<QString> directories;
    auto add = [&](NSURL *url) {
        if (!url) return;
        const auto path = QDir::cleanPath(text(url.path));
        struct stat st{};
        if (lstat(QFile::encodeName(path).constData(), &st) != 0) {
            if (errno == ENOENT) return;
            posixError(path);
        }
        if (!S_ISDIR(st.st_mode) || st.st_uid != getuid() || (st.st_mode & 0022) != 0)
            fail("ACCESS", "unsafe system trash directory: " + path);
        directories.insert(path);
    };
    add([NSURL fileURLWithPath:[NSHomeDirectory() stringByAppendingPathComponent:@".Trash"] isDirectory:YES]);
    NSArray<NSURL *> *volumes = [NSFileManager.defaultManager mountedVolumeURLsIncludingResourceValuesForKeys:nil options:0];
    for (NSURL *volume in volumes) {
        NSURL *url = [NSFileManager.defaultManager URLForDirectory:NSTrashDirectory inDomain:NSUserDomainMask appropriateForURL:volume create:NO error:nil];
        add(url);
    }
    return directories;
}

QString itemPath(const QJsonObject &params, const QSet<QString> &directories) {
    auto dir = params.value("trashDir").toString();
    if (dir.isEmpty()) dir = text([NSHomeDirectory() stringByAppendingPathComponent:@".Trash"]);
    dir = absolutePath(dir);
    if (!directories.contains(dir)) fail("INVALID", "not a current user's system trash directory");
    const auto name = params.value("name").toString();
    if (name.isEmpty() || name == "." || name == ".." || name.contains('/') || name.contains(QChar::Null))
        fail("INVALID", "invalid trash item name");
    return dir + '/' + name;
}

QJsonObject entry(const QString &directory, const QString &path) {
    const auto st = status(path);
    const auto saved = metadata(directory, path, st);
    return {{"name", QFileInfo(path).fileName()}, {"originalPath", saved.value("originalPath").toString()},
            {"deletionDate", saved.value("deletionDate").toString()}, {"trashDir", QFileInfo(path).path()},
            {"filesPath", path}, {"infoPath", saved.value("infoPath").toString()},
            {"size", static_cast<qint64>(st.st_size)}, {"isDir", S_ISDIR(st.st_mode)}, {"identity", identity(st)}};
}

QJsonArray list(const QString &directory, const QSet<QString> &directories) {
    QJsonArray result;
    auto sorted = directories.values();
    std::sort(sorted.begin(), sorted.end());
    for (const auto &dir : sorted) {
        NSError *error = nil;
        NSArray<NSURL *> *urls = [NSFileManager.defaultManager contentsOfDirectoryAtURL:[NSURL fileURLWithPath:string(dir) isDirectory:YES]
                                                        includingPropertiesForKeys:nil options:0 error:&error];
        if (!urls) fail("ACCESS", text(error.localizedDescription));
        for (NSURL *url in urls) {
            if ([url.lastPathComponent isEqualToString:@".DS_Store"]) continue;
            const auto path = text(url.path);
            if (present(path)) result.append(entry(directory, path));
        }
    }
    return result;
}

void forget(const QString &directory, const QString &path) {
    QFile file(metadataPath(directory, path));
    if (file.exists() && !file.remove()) fail("EIO", file.errorString());
    for (const auto &name : QDir(directory).entryList({"pending-*.json"}, QDir::Files | QDir::NoSymLinks)) {
        const auto pending = directory + '/' + name;
        if (readSaved(pending).value("filesPath").toString() == path && !QFile::remove(pending))
            fail("EIO", "cannot remove trash recovery metadata");
    }
}

void validateIdentity(const QJsonObject &params, const QString &path) {
    const auto expected = params.value("identity").toString();
    if (expected.isEmpty() || expected != identity(status(path))) fail("INVALID", "trash item has changed");
}

void removeItem(const QString &directory, const QJsonObject &params, const QSet<QString> &directories) {
    const auto path = itemPath(params, directories);
    validateIdentity(params, path);
    const auto saved = metadata(directory, path, status(path));
    NSError *error = nil;
    if (![NSFileManager.defaultManager removeItemAtURL:[NSURL fileURLWithPath:string(path)] error:&error])
        fail("EIO", text(error.localizedDescription));
    if (!saved.value("infoPath").toString().isEmpty()) QFile::remove(saved.value("infoPath").toString());
    try { forget(directory, path); } catch (const std::exception &) {}
}

void moveExclusive(const QString &source, const QString &target) {
    if (renamex_np(QFile::encodeName(source).constData(), QFile::encodeName(target).constData(), RENAME_EXCL) != 0)
        posixError(target);
}

void restoreItem(const QString &directory, const QJsonObject &params, const QSet<QString> &directories) {
    const auto source = itemPath(params, directories);
    validateIdentity(params, source);
    const auto saved = metadata(directory, source, status(source));
    if (saved.value("originalPath").toString().isEmpty())
        fail("NOTSUPPORTED", "original location is unavailable for this item; move it to a chosen folder");
    auto target = params.value("target").toString();
    if (target.isEmpty()) target = saved.value("originalPath").toString();
    target = absolutePath(target);
    for (const auto &dir : directories) {
        if (target == dir || target.startsWith(dir + '/')) fail("INVALID", "restore target is inside the trash");
    }
    if (!QDir().mkpath(QFileInfo(target).path())) fail("ACCESS", "cannot create restore parent directory");
    const auto parent = QFileInfo(QFileInfo(target).path()).canonicalFilePath();
    if (parent.isEmpty()) fail("INVALID", "cannot resolve restore parent directory");
    target = QDir(parent).filePath(QFileInfo(target).fileName());
    for (const auto &dir : directories) {
        const auto canonical = QFileInfo(dir).canonicalFilePath();
        if (target == canonical || target.startsWith(canonical + '/')) fail("INVALID", "restore target is inside the trash");
    }
    validateIdentity(params, source);
    moveExclusive(source, target);
    try { forget(directory, source); } catch (const std::exception &) {}
    if (!saved.value("infoPath").toString().isEmpty()) QFile::remove(saved.value("infoPath").toString());
}

QJsonObject put(const QString &directory, const QString &requested) {
    const auto path = absolutePath(requested);
    const auto before = status(path);
    const auto parent = QFileInfo(QFileInfo(path).path()).canonicalFilePath();
    if (parent.isEmpty()) fail("INVALID", "cannot resolve trash item parent directory");
    const auto location = QDir(parent).filePath(QFileInfo(path).fileName());
    const auto metadataRoot = QFileInfo(directory).canonicalFilePath();
    if (location == metadataRoot || metadataRoot.startsWith(location + '/') || location.startsWith(metadataRoot + '/'))
        fail("INVALID", "cannot trash the trash recovery metadata directory or its contents");
    const auto directories = trashDirectories();
    for (const auto &dir : directories) {
        const auto canonical = QFileInfo(dir).canonicalFilePath();
        if (location == canonical || canonical.startsWith(location + '/') || location.startsWith(canonical + '/'))
            fail("INVALID", "cannot trash a system trash directory or its contents");
    }
    NSDateFormatter *formatter = [NSDateFormatter new];
    formatter.locale = [NSLocale localeWithLocaleIdentifier:@"en_US_POSIX"];
    formatter.dateFormat = @"yyyy-MM-dd'T'HH:mm:ss";
    const auto deletionDate = text([formatter stringFromDate:NSDate.date]);
    const auto pending = directory + "/pending-" + QUuid::createUuid().toString(QUuid::WithoutBraces) + ".json";
    QJsonObject saved{{"originalPath", path}, {"deletionDate", deletionDate}, {"identity", identity(before)}};
    QSaveFile journal(pending);
    if (!journal.open(QIODevice::WriteOnly)) fail("EIO", journal.errorString());
    journal.setPermissions(QFileDevice::ReadOwner | QFileDevice::WriteOwner);
    const auto body = QJsonDocument(saved).toJson(QJsonDocument::Compact);
    if (journal.write(body) != body.size() || !journal.commit()) fail("EIO", journal.errorString());
    NSError *error = nil;
    NSURL *result = nil;
    if (![NSFileManager.defaultManager trashItemAtURL:[NSURL fileURLWithPath:string(path)] resultingItemURL:&result error:&error]) {
        QFile::remove(pending);
        fail("EIO", text(error.localizedDescription));
    }
    if (!result) fail("EIO", "file was trashed; original location is preserved in " + pending);
    const auto actual = text(result.path);
    const auto after = status(actual);
    if (identity(before) != identity(after)) fail("EIO", "trashed item identity changed; recovery metadata is in " + pending);
    saved.insert("filesPath", actual);
    try {
        QSaveFile recovery(pending);
        if (!recovery.open(QIODevice::WriteOnly)) fail("EIO", recovery.errorString());
        recovery.setPermissions(QFileDevice::ReadOwner | QFileDevice::WriteOwner);
        const auto body = QJsonDocument(saved).toJson(QJsonDocument::Compact);
        if (recovery.write(body) != body.size() || !recovery.commit()) fail("EIO", recovery.errorString());
        writeMetadata(directory, actual, saved);
        QFile::remove(pending);
    } catch (const std::exception &) {
        if (readMetadata(pending, actual, after).isEmpty())
            fail("EIO", "file was trashed; recovery metadata could not be completed: " + pending);
    }
    return entry(directory, actual);
}
}

QJsonValue trash(const QString &method, const QJsonObject &params) {
    @autoreleasepool {
        const auto directory = metadataDirectory();
        QLockFile lock(directory + "/lock");
        if (!lock.tryLock(30000)) fail("EIO", "trash metadata is busy");
        if (method == "trash.put") return put(directory, params.value("path").toString());
        const auto directories = trashDirectories();
        if (method == "trash.list") return list(directory, directories);
        if (method == "trash.count") return list(directory, directories).size();
        if (method == "trash.get") return entry(directory, itemPath(params, directories));
        if (method == "trash.restore") {
            restoreItem(directory, params, directories);
            return true;
        }
        if (method == "trash.forget") {
            const auto path = absolutePath(params.value("path").toString());
            if (!directories.contains(QFileInfo(path).path())) return true;
            if (present(path)) fail("INVALID", "trash item still exists");
            forget(directory, path);
            return true;
        }
        if (method == "trash.remove") {
            removeItem(directory, params, directories);
            return true;
        }
        if (method == "trash.empty") {
            const auto entries = list(directory, directories);
            for (const auto &value : entries) removeItem(directory, value.toObject(), directories);
            return true;
        }
        fail("NOTSUPPORTED", "unsupported trash method: " + method);
    }
}
