#include "../native/clipboard.mm"

#include <QBuffer>
#include <QCoreApplication>
#include <QImage>
#include <QProcess>
#include <QTemporaryDir>
#include <cstdio>

static void require(bool condition, const char *message) {
    if (!condition) qFatal("%s", message);
}

static Ownership ownership(const QString &directory) {
    return Ownership(open(directory.toUtf8().constData(), O_RDONLY | O_DIRECTORY | O_NOFOLLOW | O_CLOEXEC));
}

int main(int argc, char **argv) {
    QCoreApplication app(argc, argv);
    @autoreleasepool {
        const auto args = app.arguments();
        if (args.size() == 4 && args[1] == QStringLiteral("--read")) {
            auto state = ownership(args[3]);
            const auto result = readFiles([NSPasteboard pasteboardWithName:ns(args[2])], state);
            const auto bytes = QJsonDocument(result.toObject()).toJson(QJsonDocument::Compact);
            return std::fwrite(bytes.constData(), 1, bytes.size(), stdout) == static_cast<size_t>(bytes.size()) ? 0 : 1;
        }
        QTemporaryDir directory;
        require(directory.isValid(), "Clipboard state needs a temporary directory");
        NSPasteboard *board = [NSPasteboard pasteboardWithUniqueName];
        require(board != nil && ![board.name isEqualToString:NSPasteboardNameGeneral], "Native tests need an isolated pasteboard");
        const auto read = [&] {
            auto state = ownership(directory.path());
            return readFiles(board, state).toObject();
        };
        const auto copy = [&](const QJsonArray &paths, bool cut) {
            auto state = ownership(directory.path());
            return copyFiles({{"paths", paths}, {"cut", cut}}, board, state);
        };
        const auto clear = [&] {
            auto state = ownership(directory.path());
            return clearFiles(board, state).toObject().value("cleared").toBool();
        };
        const QJsonArray paths{directory.filePath(QStringLiteral("雪 #100%.txt")), directory.filePath(QStringLiteral("second.txt"))};
        copy(paths, false);
        require(read().value("paths").toArray() == paths && !read().value("cut").toBool(), "Native copy must preserve Unicode file URLs");
        NSArray<NSURL *> *urls = [board readObjectsForClasses:@[[NSURL class]] options:@{NSPasteboardURLReadingFileURLsOnlyKey: @YES}];
        require(urls.count == 2 && qt(urls[0].path) == paths[0].toString(), "Other applications must read native file URL objects");
        copy(paths, true);
        QProcess child;
        child.start(app.applicationFilePath(), {QStringLiteral("--read"), qt(board.name), directory.path()});
        require(child.waitForFinished(10000) && child.exitStatus() == QProcess::NormalExit && child.exitCode() == 0, "Separate helper process must read the selection");
        const auto externalRead = QJsonDocument::fromJson(child.readAllStandardOutput()).object();
        require(externalRead.value("cut").toBool() && externalRead.value("paths").toArray() == paths, "Cut ownership must survive across processes");
        const auto beforeInvalid = board.changeCount;
        try {
            copy(QJsonArray{QStringLiteral("relative.txt")}, false);
            qFatal("Relative paths must be rejected");
        } catch (const std::runtime_error &) {}
        require(board.changeCount == beforeInvalid && read().value("cut").toBool(), "Invalid copy must preserve the clipboard");
        [board clearContents];
        require([board writeObjects:urls], "Foreign file selection must publish");
        const auto foreign = read();
        require(foreign.value("paths").toArray() == paths && !foreign.value("cut").toBool(), "Identical foreign files must not inherit cut intent");
        const auto foreignCount = board.changeCount;
        require(!clear() && board.changeCount == foreignCount, "Clear must preserve a newer foreign selection");
        copy(paths, true);
        require(clear() && read().value("paths").toArray().isEmpty(), "Clear must remove its own selection");
        copy(paths, true);
        [board clearContents];
        require([board setString:@"newer text" forType:NSPasteboardTypeString], "Text replacement must publish");
        require(!clear() && [[board stringForType:NSPasteboardTypeString] isEqualToString:@"newer text"], "Clear must preserve newer text");

        QImage image(16, 8, QImage::Format_RGB32);
        image.fill(Qt::red);
        QByteArray encoded;
        QBuffer buffer(&encoded);
        require(buffer.open(QIODevice::WriteOnly) && image.save(&buffer, "PNG"), "Image fixture must encode");
        NSData *png = [NSData dataWithBytes:encoded.constData() length:encoded.size()];
        const auto checkImage = [&] {
            const auto result = readImage(board).toObject();
            const auto decoded = QImage::fromData(QByteArray::fromBase64(result.value("data").toString().toLatin1()), "PNG");
            require(result.value("mime") == QStringLiteral("image/png") && decoded.size() == image.size() && decoded.pixelColor(4, 4) == QColor(Qt::red), "Native image paste must return actual PNG pixels");
        };
        [board clearContents];
        require([board setData:png forType:NSPasteboardTypePNG], "Native PNG must publish");
        checkImage();
        NSBitmapImageRep *bitmap = [NSBitmapImageRep imageRepWithData:png];
        NSData *tiff = [bitmap representationUsingType:NSBitmapImageFileTypeTIFF properties:@{}];
        [board clearContents];
        require(tiff.length && [board setData:tiff forType:NSPasteboardTypeTIFF], "Native TIFF must publish");
        checkImage();
        [board clearContents];
        require([board setData:[NSData dataWithBytes:"invalid" length:7] forType:NSPasteboardTypePNG], "Invalid image fixture must publish");
        require(readImage(board).toObject().isEmpty(), "Invalid images must not produce paste data");
        [board releaseGlobally];
        qInfo("Isolated native clipboard checks passed");
    }
}
