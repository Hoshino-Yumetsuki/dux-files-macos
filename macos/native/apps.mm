#include "native.h"

#import <AppKit/AppKit.h>
#import <CoreServices/CoreServices.h>
#import <UniformTypeIdentifiers/UniformTypeIdentifiers.h>
#include <QCoreApplication>
#include <QDir>
#include <QFileInfo>
#include <QJsonArray>
#include <QSet>
#include <algorithm>
#include <stdexcept>

namespace {
QString text(NSString *value) { return value ? QString::fromUtf8(value.UTF8String) : QString(); }
NSString *string(const QString &value) { return [NSString stringWithUTF8String:value.toUtf8().constData()]; }
NSString *const duxBundleID = @"dev.duxlabs.files";

void launchServicesError(const QString &operation, NSError *error) {
    const auto detail = error ? text(error.localizedDescription) + QStringLiteral(" (%1:%2)").arg(text(error.domain)).arg(error.code)
                              : QStringLiteral("unknown LaunchServices error");
    throw std::runtime_error((QStringLiteral("EIO: ") + operation + QStringLiteral(": ") + detail).toStdString());
}
void setDefaultApplication(NSURL *url, UTType *type, NSString *urlScheme) {
    NSObject *lock = [NSObject new];
    __block bool finished = false;
    __block NSError *failure = nil;
    void (^completion)(NSError *) = ^(NSError *error) {
        @synchronized (lock) {
            failure = error;
            finished = true;
        }
    };
    if (urlScheme)
        [NSWorkspace.sharedWorkspace setDefaultApplicationAtURL:url toOpenURLsWithScheme:urlScheme completionHandler:completion];
    else
        [NSWorkspace.sharedWorkspace setDefaultApplicationAtURL:url toOpenContentType:type completionHandler:completion];

    const NSTimeInterval deadline = NSProcessInfo.processInfo.systemUptime + 120;
    for (;;) {
        @synchronized (lock) {
            if (finished) {
                if (failure) {
                    const auto detail = text(failure.localizedDescription) + QStringLiteral(" (%1:%2)").arg(text(failure.domain)).arg(failure.code);
                    throw std::runtime_error((QStringLiteral("EACCES: LaunchServices refused the default application change: ") + detail).toStdString());
                }
                return;
            }
        }
        if (NSProcessInfo.processInfo.systemUptime >= deadline)
            throw std::runtime_error("ETIMEDOUT: default application change timed out awaiting system consent; outcome is unknown");
        [NSRunLoop.currentRunLoop runMode:NSDefaultRunLoopMode beforeDate:[NSDate dateWithTimeIntervalSinceNow:0.05]];
    }
}


bool bundleID(const QString &id) {
    if (id.isEmpty()) return false;
    for (const auto ch : id) {
        const auto c = ch.unicode();
        if ((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '.' || c == '-') continue;
        return false;
    }
    return true;
}

NSURL *duxApplicationURL() {
    const auto executable = QFileInfo(QCoreApplication::applicationFilePath()).canonicalFilePath();
    const auto macos = QFileInfo(executable).dir();
    const auto contents = QFileInfo(macos.absolutePath()).dir();
    const auto path = contents.absoluteFilePath(QStringLiteral(".."));
    NSURL *url = [NSURL fileURLWithPath:string(QDir::cleanPath(path)) isDirectory:YES];
    NSBundle *bundle = [NSBundle bundleWithURL:url];
    if (macos.dirName() != QStringLiteral("MacOS") || contents.dirName() != QStringLiteral("Contents") ||
        ![url.pathExtension isEqualToString:@"app"] || ![bundle.bundleIdentifier isEqualToString:duxBundleID] ||
        ![bundle.executableURL.URLByResolvingSymlinksInPath.URLByStandardizingPath.path.stringByDeletingLastPathComponent isEqualToString:string(macos.path())] ||
        ![[NSFileManager defaultManager] isExecutableFileAtPath:bundle.executableURL.path])
        throw std::runtime_error("NOTSUPPORTED: folder handler requires the installed Dux Files.app bundle");
    return url;
}

QString folderHandler() {
    NSURL *url = [NSWorkspace.sharedWorkspace URLForApplicationToOpenContentType:UTTypeFolder];
    if (!url) launchServicesError(QStringLiteral("cannot read default folder handler"), nil);
    const auto id = text([NSBundle bundleWithURL:url].bundleIdentifier);
    if (!bundleID(id)) throw std::runtime_error("EIO: default folder handler has no valid bundle identifier");
    return id;
}

QJsonValue setFolderHandler(const QJsonObject &params) {
    const auto owner = params.value(QStringLiteral("owner")).toString();
    const auto expected = params.value(QStringLiteral("expectedOwner")).toString();
    if (!bundleID(owner) || !bundleID(expected) ||
        (owner.compare(text(duxBundleID), Qt::CaseInsensitive) != 0 && expected.compare(text(duxBundleID), Qt::CaseInsensitive) != 0))
        throw std::runtime_error("EINVAL: a Dux folder handler change and its expected owner are required");
    NSURL *dux = duxApplicationURL();
    NSURL *target = dux;
    if (owner.compare(text(duxBundleID), Qt::CaseInsensitive) != 0) {
        target = [NSWorkspace.sharedWorkspace URLsForApplicationsWithBundleIdentifier:string(owner)].firstObject;
        if (!target) launchServicesError(QStringLiteral("cannot restore saved folder handler"), nil);
        NSBundle *bundle = [NSBundle bundleWithURL:target];
        if (!bundle.bundleIdentifier || [bundle.bundleIdentifier caseInsensitiveCompare:string(owner)] != NSOrderedSame || !bundle.executableURL ||
            ![[NSFileManager defaultManager] isExecutableFileAtPath:bundle.executableURL.path])
            throw std::runtime_error("ENOENT: saved folder handler is no longer installed");
    }
    if (folderHandler().compare(expected, Qt::CaseInsensitive) != 0)
        throw std::runtime_error("EBUSY: the default folder handler changed; no preference was overwritten");
    OSStatus status = LSRegisterURL((__bridge CFURLRef)target, true);
    if (status != noErr)
        throw std::runtime_error(QStringLiteral("EIO: cannot register folder handler with LaunchServices (%1)").arg(status).toStdString());
    if (folderHandler().compare(expected, Qt::CaseInsensitive) != 0)
        throw std::runtime_error("EBUSY: the default folder handler changed; no preference was overwritten");
    setDefaultApplication(target, UTTypeFolder, nil);
    const auto current = folderHandler();
    if (current.compare(owner, Qt::CaseInsensitive) != 0)
        throw std::runtime_error((QStringLiteral("EIO: LaunchServices did not select the requested folder handler; current handler is ") + current).toStdString());
    return QJsonObject{};
}

NSURL *applicationURL(const QString &path) {
    if (!path.startsWith('/') || path.contains(QChar::Null) || !path.endsWith(QStringLiteral(".app"), Qt::CaseInsensitive)) return nil;
    NSURL *url = [NSURL fileURLWithPath:string(path) isDirectory:YES];
    NSBundle *bundle = [NSBundle bundleWithURL:url];
    if (!bundle.bundleIdentifier || !bundle.executableURL || ![[NSFileManager defaultManager] isExecutableFileAtPath:bundle.executableURL.path]) return nil;
    return url.URLByStandardizingPath;
}

QString icon(NSURL *url) {
    NSImage *image = [NSWorkspace.sharedWorkspace iconForFile:url.path];
    NSBitmapImageRep *bitmap = [[NSBitmapImageRep alloc] initWithBitmapDataPlanes:nullptr pixelsWide:32 pixelsHigh:32 bitsPerSample:8 samplesPerPixel:4 hasAlpha:YES isPlanar:NO colorSpaceName:NSCalibratedRGBColorSpace bytesPerRow:0 bitsPerPixel:0];
    if (!image || !bitmap) return {};
    [NSGraphicsContext saveGraphicsState];
    [NSGraphicsContext setCurrentContext:[NSGraphicsContext graphicsContextWithBitmapImageRep:bitmap]];
    [image drawInRect:NSMakeRect(0, 0, 32, 32) fromRect:NSZeroRect operation:NSCompositingOperationCopy fraction:1];
    [NSGraphicsContext restoreGraphicsState];
    NSData *data = [bitmap representationUsingType:NSBitmapImageFileTypePNG properties:@{}];
    return data.length ? QStringLiteral("data:image/png;base64,") + QString::fromLatin1(QByteArray(static_cast<const char *>(data.bytes), data.length).toBase64()) : QString();
}

QJsonObject entry(NSURL *url) {
    url = applicationURL(text(url.path));
    if (!url) return {};
    NSBundle *bundle = [NSBundle bundleWithURL:url];
    NSString *name = [bundle objectForInfoDictionaryKey:@"CFBundleDisplayName"] ?: [bundle objectForInfoDictionaryKey:@"CFBundleName"] ?: url.lastPathComponent.stringByDeletingPathExtension;
    const bool hidden = [[bundle objectForInfoDictionaryKey:@"LSBackgroundOnly"] boolValue];
    if (hidden) return {};
    return {{QStringLiteral("id"), text(url.path)}, {QStringLiteral("name"), text(name)},
            {QStringLiteral("icon"), icon(url)}, {QStringLiteral("terminal"), false}, {QStringLiteral("noDisplay"), false}};
}

UTType *contentType(const QString &mime) {
    if (mime == QStringLiteral("inode/directory")) return UTTypeFolder;
    if (mime == QStringLiteral("application/octet-stream")) return UTTypeData;
    return [UTType typeWithMIMEType:string(mime)];
}

QString scheme(const QString &mime) {
    const QString prefix = QStringLiteral("x-scheme-handler/");
    if (!mime.startsWith(prefix)) return {};
    const auto value = mime.mid(prefix.size());
    if (value.isEmpty() || !value.front().isLetter()) throw std::runtime_error("EINVAL: invalid URL scheme");
    for (const auto ch : value) {
        if (ch.unicode() > 127 || (!ch.isLetterOrNumber() && ch != '+' && ch != '-' && ch != '.'))
            throw std::runtime_error("EINVAL: invalid URL scheme");
    }
    return value;
}

QJsonArray sortedEntries(const QList<NSURL *> &urls, const QString &defaultID = {}) {
    QList<QJsonObject> entries;
    QSet<QString> seen;
    for (NSURL *url : urls) {
        const auto app = entry(url);
        const auto id = app.value(QStringLiteral("id")).toString();
        if (id.isEmpty() || seen.contains(id)) continue;
        seen.insert(id);
        entries.append(app);
    }
    std::sort(entries.begin(), entries.end(), [&](const QJsonObject &a, const QJsonObject &b) {
        const bool ad = a.value(QStringLiteral("id")) == defaultID;
        const bool bd = b.value(QStringLiteral("id")) == defaultID;
        if (ad != bd) return ad;
        return a.value(QStringLiteral("name")).toString().compare(b.value(QStringLiteral("name")).toString(), Qt::CaseInsensitive) < 0;
    });
    QJsonArray result;
    for (const auto &app : entries) result.append(app);
    return result;
}

QList<NSURL *> applications() {
    NSFileManager *manager = NSFileManager.defaultManager;
    NSMutableArray<NSURL *> *roots = [[manager URLsForDirectory:NSApplicationDirectory inDomains:NSAllDomainsMask] mutableCopy];
    [roots addObject:[NSURL fileURLWithPath:@"/System/Applications" isDirectory:YES]];
    [roots addObject:[NSURL fileURLWithPath:@"/System/Library/CoreServices/Applications" isDirectory:YES]];
    QList<NSURL *> result;
    for (NSURL *root in roots) {
        NSDirectoryEnumerator *enumerator = [manager enumeratorAtURL:root includingPropertiesForKeys:nil options:NSDirectoryEnumerationSkipsHiddenFiles | NSDirectoryEnumerationSkipsPackageDescendants errorHandler:nil];
        for (NSURL *url in enumerator) {
            if ([url.pathExtension caseInsensitiveCompare:@"app"] == NSOrderedSame) result.append(url);
        }
    }
    return result;
}

QJsonValue query(const QJsonObject &params) {
    QJsonObject associations, defaults;
    for (const auto value : params.value(QStringLiteral("mimes")).toArray()) {
        const auto mime = value.toString();
        if (!value.isString() || mime.isEmpty() || mime.contains(QChar::Null)) throw std::runtime_error("EINVAL: invalid MIME type");
        QList<NSURL *> urls;
        NSURL *preferred = nil;
        const auto urlScheme = scheme(mime);
        if (!urlScheme.isEmpty()) {
            NSURL *url = [NSURL URLWithString:[string(urlScheme) stringByAppendingString:@"://dux.invalid/"]];
            preferred = [NSWorkspace.sharedWorkspace URLForApplicationToOpenURL:url];
            for (NSURL *app in [NSWorkspace.sharedWorkspace URLsForApplicationsToOpenURL:url]) urls.append(app);
        } else {
            UTType *type = contentType(mime);
            if (type) {
                preferred = [NSWorkspace.sharedWorkspace URLForApplicationToOpenContentType:type];
                for (NSURL *app in [NSWorkspace.sharedWorkspace URLsForApplicationsToOpenContentType:type]) urls.append(app);
            }
        }
        const auto preferredEntry = entry(preferred);
        const auto defaultID = preferredEntry.value(QStringLiteral("id")).toString();
        if (preferred) urls.append(preferred);
        defaults.insert(mime, defaultID);
        associations.insert(mime, sortedEntries(urls, defaultID));
    }
    return QJsonObject{{QStringLiteral("apps"), params.value(QStringLiteral("includeAll")).toBool() ? sortedEntries(applications()) : QJsonArray{}},
                       {QStringLiteral("associations"), associations}, {QStringLiteral("defaults"), defaults}};
}

QJsonValue setDefault(const QJsonObject &params) {
    const auto mime = params.value(QStringLiteral("mime")).toString();
    NSURL *url = applicationURL(params.value(QStringLiteral("id")).toString());
    if (!url || mime.isEmpty() || mime.contains(QChar::Null)) throw std::runtime_error("EINVAL: a MIME type and an installed .app bundle are required");
    const auto urlScheme = scheme(mime);
    UTType *type = urlScheme.isEmpty() ? contentType(mime) : nil;
    if (urlScheme.isEmpty() && !type) throw std::runtime_error("NOTSUPPORTED: MIME type has no native content type");
    OSStatus status = LSRegisterURL((__bridge CFURLRef)url, true);
    if (status != noErr) throw std::runtime_error("EIO: cannot register application with LaunchServices");
    setDefaultApplication(url, type, urlScheme.isEmpty() ? nil : string(urlScheme));
    return QJsonObject{};
}
}

QJsonValue apps(const QString &method, const QJsonObject &params) {
    @autoreleasepool {
        if (method == QStringLiteral("apps.folderHandler.get")) {
            duxApplicationURL();
            return QJsonObject{{QStringLiteral("owner"), folderHandler()}};
        }
        if (method == QStringLiteral("apps.folderHandler.set")) return setFolderHandler(params);
        if (method == QStringLiteral("apps.query")) return query(params);
        if (method == QStringLiteral("apps.mime")) {
            const auto path = params.value(QStringLiteral("path")).toString();
            if (!path.startsWith('/') || path.contains(QChar::Null)) throw std::runtime_error("EINVAL: an absolute local path is required");
            NSURL *url = [NSURL fileURLWithPath:string(path)];
            UTType *type = nil;
            NSError *error = nil;
            if (![url getResourceValue:&type forKey:NSURLContentTypeKey error:&error])
                throw std::runtime_error((QStringLiteral("EIO: ") + text(error.localizedDescription)).toStdString());
            return QJsonObject{{QStringLiteral("mime"), text(type.preferredMIMEType)}};
        }
        if (method == QStringLiteral("apps.entry")) {
            const auto result = entry(applicationURL(params.value(QStringLiteral("id")).toString()));
            return result.isEmpty() ? QJsonValue(QJsonValue::Null) : QJsonValue(result);
        }
        if (method == QStringLiteral("apps.setDefault")) return setDefault(params);
        throw std::runtime_error("NOTSUPPORTED: unknown apps method");
    }
}
