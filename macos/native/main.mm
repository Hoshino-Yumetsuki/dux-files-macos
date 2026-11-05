#include "native.h"
#include "../runtime/shell.h"

#import <AppKit/AppKit.h>
#include <QBuffer>
#include <QUrl>
#include <QFileInfo>
#include <QCoreApplication>
#include <QFile>
#include <QJsonArray>
#include <QJsonDocument>
#include <stdexcept>

namespace {
constexpr qsizetype maxRequest = 16 * 1024 * 1024;
constexpr qsizetype maxReply = 64 * 1024 * 1024;

bool writeValue(const QJsonValue &value) {
    auto data = QJsonDocument(QJsonArray{value}).toJson(QJsonDocument::Compact);
    data = data.mid(1, data.size() - 2) + '\n';
    if (data.size() > maxReply) return false;
    QFile output;
    if (!output.open(stdout, QIODevice::WriteOnly)) return false;
    qsizetype offset = 0;
    while (offset < data.size()) {
        const auto count = output.write(data.constData() + offset, data.size() - offset);
        if (count <= 0) return false;
        offset += count;
    }
    return output.flush();
}

int failure(const QString &message) {
    const auto separator = message.indexOf(QStringLiteral(": "));
    const auto rawCode = separator > 0 && separator < 24 ? message.left(separator) : QStringLiteral("EIO");
    const QHash<QString, QString> aliases{{"INVALID", "EINVAL"}, {"IO", "EIO"}, {"ACCESS", "EACCES"},
        {"EXISTS", "EEXIST"}, {"NOTFOUND", "ENOENT"}};
    const auto code = aliases.value(rawCode, rawCode);
    const auto detail = separator > 0 && separator < 24 ? message.mid(separator + 2) : message;
    writeValue(QJsonObject{{"code", code}, {"error", detail}});
    return 1;
}

QJsonValue dispatch(const QString &method, const QJsonObject &params) {
    if (method == QStringLiteral("appearance.get")) {
        NSAppearanceName name = [NSApp.effectiveAppearance bestMatchFromAppearancesWithNames:@[NSAppearanceNameAqua, NSAppearanceNameDarkAqua]];
        return QJsonObject{{"available", name != nil}, {"colorScheme", [name isEqualToString:NSAppearanceNameDarkAqua] ? 1 : 2}};
    }
    if (method == QStringLiteral("media.thumbnail")) {
        const auto path = params.value(QStringLiteral("path")).toString();
        const auto size = params.value(QStringLiteral("size")).toInt();
        if (!QFileInfo(path).isAbsolute() || path.contains(QChar::Null) || size < 1 || size > 1024)
            throw std::runtime_error("EINVAL: absolute path and size between 1 and 1024 are required");
        const auto image = DuxCocoa::thumbnail(QUrl::fromLocalFile(path), QSize(size, size));
        if (image.isNull()) throw std::runtime_error("NOTSUPPORTED: no native thumbnail for file");
        QByteArray data;
        QBuffer buffer(&data);
        buffer.open(QIODevice::WriteOnly);
        if (!image.save(&buffer, "PNG")) throw std::runtime_error("EIO: could not encode native thumbnail");
        return QJsonObject{{"data", QString::fromLatin1(data.toBase64())}};
    }
    if (method.startsWith(QStringLiteral("apps."))) return apps(method, params);
    if (method.startsWith(QStringLiteral("clipboard."))) return clipboard(method, params);
    if (method.startsWith(QStringLiteral("volumes.")) || method.startsWith(QStringLiteral("network."))) return volumes(method, params);
    if (method.startsWith(QStringLiteral("trash."))) return trash(method, params);
    if (method.startsWith(QStringLiteral("search."))) return search(method, params);
    if (method.startsWith(QStringLiteral("places."))) return places(method, params);
    if (method.startsWith(QStringLiteral("metadata."))) return metadata(method, params);
    if (method.startsWith(QStringLiteral("cloud."))) return cloud(method, params);
    if (method.startsWith(QStringLiteral("fileops."))) return fileops(method, params);
    throw std::runtime_error("NOTSUPPORTED: unknown native platform method");
}
}

void publishEvent(const QString &topic, const QJsonValue &data) {
    QJsonObject event{{"topic", topic}};
    if (!data.isUndefined() && !data.isNull()) event.insert(QStringLiteral("data"), data);
    if (!writeValue(event)) QCoreApplication::exit(1);
}

int main(int argc, char **argv) {
    @autoreleasepool {
        QCoreApplication app(argc, argv);
        app.setApplicationName(QStringLiteral("dfiles-native"));
        [NSApplication sharedApplication];
        [NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];
        @try {
            try {
                if (app.arguments() == QStringList{app.arguments().first(), QStringLiteral("--fileops-coordinate")})
                    return coordinateFileops();
                if (app.arguments() == QStringList{app.arguments().first(), QStringLiteral("--watch")}) {
                    watchVolumes();
                    auto appearance = [] {
                        publishEvent(QStringLiteral("colorScheme"), dispatch(QStringLiteral("appearance.get"), {}));
                    };
                    [NSDistributedNotificationCenter.defaultCenter addObserverForName:@"AppleInterfaceThemeChangedNotification"
                        object:nil queue:NSOperationQueue.mainQueue usingBlock:^(NSNotification *) { appearance(); }];
                    appearance();
                    return app.exec();
                }
                if (argc != 1) return failure(QStringLiteral("EINVAL: unexpected native helper argument"));
                QFile input;
                if (!input.open(stdin, QIODevice::ReadOnly)) return failure(QStringLiteral("EIO: could not open request input"));
                const auto body = input.read(maxRequest + 1);
                if (body.size() > maxRequest) return failure(QStringLiteral("EINVAL: native platform request exceeds 16 MiB"));
                QJsonParseError error;
                const auto document = QJsonDocument::fromJson(body, &error);
                if (error.error != QJsonParseError::NoError || !document.isObject()) return failure(QStringLiteral("EINVAL: invalid JSON request"));
                const auto request = document.object();
                const auto method = request.value(QStringLiteral("method"));
                const auto params = request.value(QStringLiteral("params"));
                if (!method.isString() || method.toString().isEmpty() || (!params.isUndefined() && !params.isNull() && !params.isObject()))
                    return failure(QStringLiteral("EINVAL: method and object params are required"));
                if (!writeValue(dispatch(method.toString(), params.toObject()))) return failure(QStringLiteral("EIO: native platform reply could not be written"));
                return 0;
            } catch (const std::exception &error) {
                return failure(QString::fromUtf8(error.what()));
            }
        } @catch (NSException *error) {
            return failure(QStringLiteral("EIO: ") + QString::fromUtf8(error.reason.UTF8String));
        }
    }
}
