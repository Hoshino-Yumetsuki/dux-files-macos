#include "native.h"

#import <Foundation/Foundation.h>
#include <QDir>
#include <QJsonArray>
#include <QFile>
#include <stdexcept>
#include <unistd.h>
#include <stdio.h>
#include <cerrno>
#include <cstring>

namespace {
QString text(NSString *s) { return s ? QString::fromNSString(s) : QString(); }
[[noreturn]] void fail(NSError *error, const QString &fallback) {
    throw std::runtime_error((QStringLiteral("EIO: ") + (error ? text(error.localizedDescription) : fallback)).toUtf8().constData());
}
NSURL *url(const QJsonValue &value) {
    const auto path = value.toString();
    if (!value.isString() || !QDir::isAbsolutePath(path) || path.contains(QChar::Null))
        throw std::runtime_error("EINVAL: an absolute path is required");
    return [NSURL fileURLWithPath:path.toNSString()];
}
NSArray<NSString *> *tags(NSURL *file) {
    NSError *error = nil;
    NSArray<NSString *> *value = nil;
    if (![file getResourceValue:&value forKey:NSURLTagNamesKey error:&error]) fail(error, QStringLiteral("Could not read Finder tags"));
    return value ?: @[];
}
QJsonArray jsonTags(NSArray<NSString *> *names) {
    QJsonArray result;
    for (NSString *name in names) result.append(text(name));
    return result;
}
NSArray<NSString *> *requestedTags(const QJsonValue &value) {
    if (!value.isArray()) throw std::runtime_error("EINVAL: tags must be an array");
    NSMutableArray<NSString *> *result = [NSMutableArray array];
    for (const auto &item : value.toArray()) {
        const auto name = item.toString();
        if (!item.isString() || name.trimmed().isEmpty() || name.contains(QChar::Null) || name.contains(QLatin1Char('\n')))
            throw std::runtime_error("EINVAL: invalid tag name");
        if (![result containsObject:name.toNSString()]) [result addObject:name.toNSString()];
    }
    return result;
}
bool isAlias(NSURL *file) {
    NSError *error = nil;
    NSNumber *symbolic = nil, *alias = nil;
    if (![file getResourceValue:&symbolic forKey:NSURLIsSymbolicLinkKey error:&error]) fail(error, QStringLiteral("Could not inspect file"));
    if (symbolic.boolValue) return false;
    if (![file getResourceValue:&alias forKey:NSURLIsAliasFileKey error:&error]) fail(error, QStringLiteral("Could not inspect file"));
    return alias.boolValue;
}
}

QJsonValue metadata(const QString &method, const QJsonObject &params) {
    if (method == QStringLiteral("metadata.tagsRead")) {
        const auto paths = params.value("paths");
        if (!paths.isArray() || paths.toArray().isEmpty()) throw std::runtime_error("EINVAL: paths are required");
        QJsonArray items;
        for (const auto &path : paths.toArray()) items.append(QJsonObject{{"path", path.toString()}, {"tags", jsonTags(tags(url(path)))}});
        return QJsonObject{{"items", items}};
    }
    if (method == QStringLiteral("metadata.tagsEdit")) {
        const auto paths = params.value("paths");
        const auto mode = params.value("mode").toString();
        if (!paths.isArray() || paths.toArray().isEmpty() || (mode != "add" && mode != "remove" && mode != "replace"))
            throw std::runtime_error("EINVAL: paths and tag edit mode are required");
        NSArray<NSString *> *names = requestedTags(params.value("tags"));
        QJsonArray errors;
        for (const auto &path : paths.toArray()) {
            try {
                NSURL *file = url(path);
                NSMutableArray<NSString *> *updated = mode == "replace" ? [NSMutableArray array] : [tags(file) mutableCopy];
                for (NSString *name in names) {
                    if (mode == "remove") [updated removeObject:name];
                    else if (![updated containsObject:name]) [updated addObject:name];
                }
                NSError *error = nil;
                if (![file setResourceValue:updated forKey:NSURLTagNamesKey error:&error]) fail(error, QStringLiteral("Could not write Finder tags"));
            } catch (const std::exception &e) { errors.append(QJsonObject{{"path", path.toString()}, {"error", QString::fromUtf8(e.what())}}); }
        }
        return QJsonObject{{"errors", errors}};
    }
    if (method == QStringLiteral("metadata.tagsList")) {
        NSURL *folder = url(params.value("path"));
        NSError *error = nil;
        NSArray<NSURL *> *files = [NSFileManager.defaultManager contentsOfDirectoryAtURL:folder includingPropertiesForKeys:@[NSURLTagNamesKey] options:0 error:&error];
        if (!files) fail(error, QStringLiteral("Could not list folder"));
        QJsonArray items;
        const QDir directory(params.value("path").toString());
        for (NSURL *file in files) items.append(QJsonObject{{"path", directory.filePath(text(file.lastPathComponent))}, {"tags", jsonTags(tags(file))}});
        return QJsonObject{{"items", items}};
    }
    if (method == QStringLiteral("metadata.aliasInfo")) return QJsonObject{{"isAlias", isAlias(url(params.value("path")))}};
    if (method == QStringLiteral("metadata.aliasResolve")) {
        NSURL *file = url(params.value("path"));
        if (!isAlias(file)) throw std::runtime_error("EINVAL: not a Finder alias");
        NSError *error = nil;
        NSData *data = [NSURL bookmarkDataWithContentsOfURL:file error:&error];
        if (!data) fail(error, QStringLiteral("Could not read alias"));
        BOOL stale = NO;
        NSURL *target = [NSURL URLByResolvingBookmarkData:data options:NSURLBookmarkResolutionWithoutUI | NSURLBookmarkResolutionWithoutMounting relativeToURL:nil bookmarkDataIsStale:&stale error:&error];
        if (!target || !target.isFileURL) fail(error, QStringLiteral("Could not resolve alias"));
        NSNumber *directory = nil;
        if (![target getResourceValue:&directory forKey:NSURLIsDirectoryKey error:&error]) fail(error, QStringLiteral("Alias target is unavailable"));
        return QJsonObject{{"path", text(target.path)}, {"isDirectory", bool(directory.boolValue)}, {"stale", bool(stale)}};
    }
    if (method == QStringLiteral("metadata.aliasCreate")) {
        NSURL *source = url(params.value("path"));
        NSURL *destination = url(params.value("destination"));
        NSError *error = nil;
        NSData *data = [source bookmarkDataWithOptions:NSURLBookmarkCreationSuitableForBookmarkFile includingResourceValuesForKeys:nil relativeToURL:nil error:&error];
        if (!data) fail(error, QStringLiteral("Could not create alias data"));
        NSString *templatePath = [destination.URLByDeletingLastPathComponent.path stringByAppendingPathComponent:@".dux-alias-XXXXXX"];
        QByteArray temp = QFile::encodeName(text(templatePath));
        if (!mkdtemp(temp.data())) throw std::runtime_error("EIO: could not create temporary alias directory");
        NSString *directory = [NSFileManager.defaultManager stringWithFileSystemRepresentation:temp.constData() length:strlen(temp.constData())];
        NSURL *temporary = [NSURL fileURLWithPath:[directory stringByAppendingPathComponent:@"alias"]];
        bool written = [NSURL writeBookmarkData:data toURL:temporary options:0 error:&error];
        int result = written ? renamex_np(temporary.fileSystemRepresentation, destination.fileSystemRepresentation, RENAME_EXCL) : -1;
        int saved = errno;
        [NSFileManager.defaultManager removeItemAtPath:directory error:nil];
        if (!written) fail(error, QStringLiteral("Could not create alias"));
        if (result != 0) throw std::runtime_error((QString::fromLatin1(saved == EEXIST ? "EEXIST: " : "EIO: ") + QString::fromLocal8Bit(strerror(saved))).toUtf8().constData());
        return QJsonObject{{"path", text(destination.path)}};
    }
    throw std::runtime_error("NOTSUPPORTED: unknown metadata method");
}
