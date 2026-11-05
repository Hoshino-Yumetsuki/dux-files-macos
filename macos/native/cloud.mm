#include "native.h"

#import <Foundation/Foundation.h>
#include <QDir>
#include <QJsonObject>
#include <sys/stat.h>
#include <cerrno>
#include <cstring>
#include <stdexcept>

namespace {
QString text(NSString *value) { return value ? QString::fromUtf8(value.UTF8String) : QString(); }

[[noreturn]] void fail(NSError *error, const char *fallback) {
    const QString message = error ? text(error.localizedDescription) : QString::fromUtf8(fallback);
    throw std::runtime_error((QStringLiteral("EIO: ") + message).toUtf8().constData());
}

double downloadProgress(NSURL *url) {
    NSMetadataQuery *query = [[NSMetadataQuery alloc] init];
    query.searchScopes = @[url.URLByDeletingLastPathComponent.path];
    query.predicate = [NSPredicate predicateWithFormat:@"%K == %@", NSMetadataItemPathKey, url.path];
    if (![query startQuery]) return -1;
    NSDate *deadline = [NSDate dateWithTimeIntervalSinceNow:0.25];
    while (query.isGathering && deadline.timeIntervalSinceNow > 0)
        [[NSRunLoop currentRunLoop] runMode:NSDefaultRunLoopMode beforeDate:[NSDate dateWithTimeIntervalSinceNow:0.02]];
    double progress = -1;
    [query disableUpdates];
    if (query.resultCount) {
        NSNumber *percent = [[query resultAtIndex:0] valueForAttribute:NSMetadataUbiquitousItemPercentDownloadedKey];
        if ([percent isKindOfClass:NSNumber.class] && percent.doubleValue >= 0 && percent.doubleValue <= 100)
            progress = percent.doubleValue / 100.0;
    }
    [query stopQuery];
    return progress;
}

QJsonObject status(NSURL *url, const QString &path) {
    struct stat info;
    if (lstat(url.fileSystemRepresentation, &info) != 0) {
        const int saved = errno;
        const char *code = saved == ENOENT ? "ENOENT" : saved == EACCES ? "EACCES" : saved == EPERM ? "EPERM" : saved == ENOTDIR ? "ENOTDIR" : saved == ELOOP ? "ELOOP" : "EIO";
        throw std::runtime_error((QString::fromLatin1(code) + QStringLiteral(": ") + QString::fromLocal8Bit(strerror(saved))).toUtf8().constData());
    }
    NSError *error = nil;
    id ubiquitous = nil;
    if (![url getResourceValue:&ubiquitous forKey:NSURLIsUbiquitousItemKey error:&error]) fail(error, "Cannot inspect item");
    const bool icloud = [ubiquitous isKindOfClass:NSNumber.class] && [ubiquitous boolValue];
    QJsonObject result{{QStringLiteral("path"), path},
                       {QStringLiteral("provider"), icloud ? QStringLiteral("icloud") : ((info.st_flags & SF_DATALESS) ? QStringLiteral("other") : QStringLiteral("local"))}};
    QString state = (info.st_flags & SF_DATALESS) ? QStringLiteral("dataless") : QStringLiteral("local");
    if (icloud) {
        NSDictionary *values = [url resourceValuesForKeys:@[NSURLUbiquitousItemDownloadingStatusKey, NSURLUbiquitousItemIsDownloadingKey, NSURLUbiquitousItemDownloadingErrorKey] error:&error];
        if (!values) fail(error, "Cannot inspect iCloud item");
        id errorValue = values[NSURLUbiquitousItemDownloadingErrorKey];
        NSError *downloadError = [errorValue isKindOfClass:NSError.class] ? errorValue : nil;
        id statusValue = values[NSURLUbiquitousItemDownloadingStatusKey];
        NSString *downloadStatus = [statusValue isKindOfClass:NSString.class] ? statusValue : nil;
        id downloading = values[NSURLUbiquitousItemIsDownloadingKey];
        if (downloadError) {
            state = QStringLiteral("error");
            result.insert(QStringLiteral("error"), text(downloadError.localizedDescription));
        } else if ([downloading isKindOfClass:NSNumber.class] && [downloading boolValue]) {
            state = QStringLiteral("downloading");
            const double progress = downloadProgress(url);
            if (progress >= 0) result.insert(QStringLiteral("progress"), progress);
        } else if ([downloadStatus isEqualToString:NSURLUbiquitousItemDownloadingStatusNotDownloaded]) {
            state = QStringLiteral("dataless");
        } else if (![downloadStatus isEqualToString:NSURLUbiquitousItemDownloadingStatusCurrent] &&
                   ![downloadStatus isEqualToString:NSURLUbiquitousItemDownloadingStatusDownloaded]) {
            state = QStringLiteral("error");
            result.insert(QStringLiteral("error"), QStringLiteral("iCloud download status is unavailable"));
        }
    }
    result.insert(QStringLiteral("state"), state);
    return result;
}
}

QJsonValue cloud(const QString &method, const QJsonObject &params) {
    if (method != QStringLiteral("cloud.status") && method != QStringLiteral("cloud.download"))
        throw std::runtime_error("NOTSUPPORTED: unknown cloud method");
    const auto value = params.value(QStringLiteral("path"));
    const QString path = value.toString();
    if (!value.isString() || !QDir::isAbsolutePath(path) || path.contains(QChar::Null))
        throw std::runtime_error("EINVAL: cloud operation requires an absolute path without null bytes");
    const QString cleanPath = QDir::cleanPath(path);
    NSURL *url = [NSURL fileURLWithPath:cleanPath.toNSString()];
    QJsonObject result = status(url, cleanPath);
    if (method == QStringLiteral("cloud.status") || result.value(QStringLiteral("state")) == QStringLiteral("local")) return result;
    if (result.value(QStringLiteral("provider")) == QStringLiteral("icloud")) {
        NSError *error = nil;
        if (![[NSFileManager defaultManager] startDownloadingUbiquitousItemAtURL:url error:&error]) fail(error, "Cannot start iCloud download");
        return status(url, cleanPath);
    }
    if (result.value(QStringLiteral("provider")) != QStringLiteral("other"))
        throw std::runtime_error("NOTSUPPORTED: download is unavailable for this item");
    NSFileCoordinator *coordinator = [[NSFileCoordinator alloc] initWithFilePresenter:nil];
    NSError *error = nil;
    __block bool accessed = false;
    __block QJsonObject coordinatedResult;
    [coordinator coordinateReadingItemAtURL:url options:0 error:&error byAccessor:^(NSURL *newURL) {
        coordinatedResult = status(newURL, cleanPath);
        accessed = true;
    }];
    if (error || !accessed) fail(error, "Cannot coordinate cloud download");
    result = coordinatedResult;
    if (result.value(QStringLiteral("state")) != QStringLiteral("local"))
        throw std::runtime_error("EIO: provider did not materialize the item");
    return result;
}
