#include "native.h"

#import <Foundation/Foundation.h>
#import <CoreServices/CoreServices.h>
#include <QDir>
#include <QJsonArray>
#include <chrono>
#include <stdexcept>

namespace {
NSString *string(const QString &value) { return [NSString stringWithUTF8String:value.toUtf8().constData()]; }

struct Query {
    MDQueryRef value;
    ~Query() {
        if (!value) return;
        MDQueryStop(value);
        CFRelease(value);
    }
};
}

QJsonValue search(const QString &method, const QJsonObject &params) {
    if (method != QStringLiteral("search.query")) throw std::runtime_error("NOTSUPPORTED: unknown search method");
    const auto rootsValue = params.value(QStringLiteral("roots"));
    const auto predicateValue = params.value(QStringLiteral("predicate"));
    const auto limitValue = params.value(QStringLiteral("limit"));
    const auto roots = rootsValue.toArray();
    const auto predicate = predicateValue.toString();
    const int limit = limitValue.toInt();
    if (!rootsValue.isArray() || roots.isEmpty() || roots.size() > 128 || !predicateValue.isString() ||
        predicate.isEmpty() || predicate.size() > 65536 || predicate.contains(QChar::Null) ||
        !limitValue.isDouble() || limit < 1 || limit > 10000 || limitValue.toDouble() != limit)
        throw std::runtime_error("EINVAL: search requires absolute roots, a predicate and a limit from 1 to 10000");
    NSMutableArray<NSString *> *scope = [NSMutableArray arrayWithCapacity:roots.size()];
    for (const auto &value : roots) {
        const auto root = value.toString();
        if (!value.isString() || !QDir::isAbsolutePath(root) || root.contains(QChar::Null))
            throw std::runtime_error("EINVAL: search roots must be absolute paths without null bytes");
        [scope addObject:string(QDir::cleanPath(root))];
    }
    Query query{MDQueryCreate(kCFAllocatorDefault, (__bridge CFStringRef)string(predicate), nullptr, nullptr)};
    if (!query.value) throw std::runtime_error("EINVAL: invalid Spotlight predicate");
    MDQuerySetSearchScope(query.value, (__bridge CFArrayRef)scope, 0);
    MDQuerySetMaxCount(query.value, limit + 1);
    if (!MDQueryExecute(query.value, 0)) throw std::runtime_error("NOTSUPPORTED: Spotlight search is unavailable");
    const auto deadline = std::chrono::steady_clock::now() + std::chrono::seconds(10);
    while (!MDQueryIsGatheringComplete(query.value) && MDQueryGetResultCount(query.value) <= limit && std::chrono::steady_clock::now() < deadline)
        CFRunLoopRunInMode(kCFRunLoopDefaultMode, 0.05, true);
    const bool complete = MDQueryIsGatheringComplete(query.value);
    MDQueryStop(query.value);
    const auto count = MDQueryGetResultCount(query.value);
    QJsonArray paths;
    bool partial = !complete || count > limit;
    qsizetype bytes = 0;
    for (CFIndex i = 0; i < count && paths.size() < limit; ++i) {
        const auto item = static_cast<MDItemRef>(const_cast<void *>(MDQueryGetResultAtIndex(query.value, i)));
        NSString *path = CFBridgingRelease(MDItemCopyAttribute(item, kMDItemPath));
        if (!path) continue;
        const auto value = QString::fromUtf8(path.UTF8String);
        // JSON escaping can grow each UTF-8 byte sixfold; stay below the bridge's 64 MiB reply cap.
        bytes += value.toUtf8().size() * 6 + 4;
        if (bytes > 48 * 1024 * 1024) {
            partial = true;
            break;
        }
        paths.append(value);
    }
    return QJsonObject{{QStringLiteral("paths"), paths}, {QStringLiteral("partial"), partial}};
}
