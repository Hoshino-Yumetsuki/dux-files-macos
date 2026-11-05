#import <Foundation/Foundation.h>
#include <QFile>
#include <QFileInfo>
#include <QJsonDocument>
#include <QJsonObject>
#include <QUrl>
#include <unistd.h>
#include <cstdio>

int coordinateFileops() {
    QFile input;
    if (!input.open(stdin, QIODevice::ReadOnly | QIODevice::Unbuffered)) return 1;
    const auto line = input.readLine(1024 * 1024);
    const auto request = QJsonDocument::fromJson(line).object();
    const auto src = request.value("src").toString();
    const auto dst = request.value("dst").toString();
    if (!QFileInfo(src).isAbsolute() || !QFileInfo(dst).isAbsolute() || src.contains(QChar::Null) || dst.contains(QChar::Null)) return 1;
    NSURL *source = [NSURL fileURLWithPath:src.toNSString()];
    NSURL *destination = [NSURL fileURLWithPath:dst.toNSString()];
    NSFileCoordinator *coordinator = [[NSFileCoordinator alloc] initWithFilePresenter:nil];
    __block bool entered = false;
    void (^lease)(NSURL *, NSURL *) = ^(NSURL *from, NSURL *to) {
        entered = true;
        QJsonObject paths{{"src", QString::fromNSString(from.path)}, {"dst", QString::fromNSString(to.path)}};
        const auto data = QJsonDocument(paths).toJson(QJsonDocument::Compact) + '\n';
        fwrite(data.constData(), 1, size_t(data.size()), stdout);
        fflush(stdout);
        char buffer[256];
        while (::read(STDIN_FILENO, buffer, sizeof(buffer)) > 0) {}
    };
    NSError *error = nil;
    if (request.value("move").toBool()) {
        [coordinator coordinateWritingItemAtURL:source options:NSFileCoordinatorWritingForMoving
                              writingItemAtURL:destination options:0 error:&error byAccessor:lease];
    } else {
        [coordinator coordinateReadingItemAtURL:source options:0 writingItemAtURL:destination options:0 error:&error byAccessor:lease];
    }
    if (!entered) {
        QJsonObject failure{{"error", error ? QString::fromNSString(error.localizedDescription) : QStringLiteral("file coordination failed")}};
        const auto data = QJsonDocument(failure).toJson(QJsonDocument::Compact) + '\n';
        fwrite(data.constData(), 1, size_t(data.size()), stdout);
        fflush(stdout);
        return 1;
    }
    return 0;
}
