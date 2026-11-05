#include "native.h"

#import <AppKit/AppKit.h>
#import <ImageIO/ImageIO.h>

#include <QJsonArray>
#include <QJsonDocument>
#include <QByteArray>
#include <fcntl.h>
#include <sys/file.h>
#include <sys/stat.h>
#include <unistd.h>
#include <cerrno>
#include <cstring>
#include <stdexcept>

namespace {
constexpr qsizetype maxPaths = 10000;
constexpr qsizetype maxSelection = 1024 * 1024;
constexpr NSUInteger maxImage = 32 * 1024 * 1024;
constexpr uint64_t maxImagePixels = 32 * 1024 * 1024;
NSString *const markerType = @"dev.duxlabs.files.file-selection";
NSString *ns(const QString &value) { return [NSString stringWithUTF8String:value.toUtf8().constData()]; }
QString qt(NSString *value) { return value ? QString::fromUtf8(value.UTF8String) : QString(); }

struct File {
    int fd;
    explicit File(int value) : fd(value) { if (fd < 0) throw std::runtime_error(std::strerror(errno)); }
    ~File() { close(fd); }
    File(const File &) = delete;
    File &operator=(const File &) = delete;
};

class Ownership {
    File directory;
    File file;
    static int openDirectory() {
        char root[PATH_MAX];
        const auto length = confstr(_CS_DARWIN_USER_CACHE_DIR, root, sizeof(root));
        if (!length || length > sizeof(root))
            throw std::runtime_error("cannot locate private clipboard cache");
        File parent(open(root, O_RDONLY | O_DIRECTORY | O_CLOEXEC));
        if (mkdirat(parent.fd, "dev.duxlabs.files.clipboard", 0700) < 0 && errno != EEXIST)
            throw std::runtime_error(std::strerror(errno));
        int descriptor = openat(parent.fd, "dev.duxlabs.files.clipboard", O_RDONLY | O_DIRECTORY | O_NOFOLLOW | O_CLOEXEC);
        if (descriptor < 0) throw std::runtime_error(std::strerror(errno));
        struct stat info;
        if (fstat(descriptor, &info) < 0 || info.st_uid != getuid() || (info.st_mode & 0077)) {
            close(descriptor);
            throw std::runtime_error("clipboard ownership directory must be private");
        }
        return descriptor;
    }
public:
    explicit Ownership(int descriptor = openDirectory()) : directory(descriptor), file(openat(directory.fd, "selection", O_RDWR | O_CREAT | O_NOFOLLOW | O_CLOEXEC, 0600)) {
        struct stat info;
        if (fstat(file.fd, &info) < 0 || !S_ISREG(info.st_mode) || info.st_uid != getuid() || info.st_nlink != 1 || (info.st_mode & 0077))
            throw std::runtime_error("clipboard ownership file must be private");
        if (flock(file.fd, LOCK_EX) < 0) throw std::runtime_error(std::strerror(errno));
    }
    QJsonObject read() const {
        struct stat info;
        if (fstat(file.fd, &info) < 0 || info.st_size <= 0 || info.st_size > maxSelection) return {};
        QByteArray data(static_cast<qsizetype>(info.st_size), Qt::Uninitialized);
        if (pread(file.fd, data.data(), static_cast<size_t>(data.size()), 0) != data.size()) return {};
        return QJsonDocument::fromJson(data).object();
    }
    void write(const QJsonObject &state) const {
        const auto data = state.isEmpty() ? QByteArray() : QJsonDocument(state).toJson(QJsonDocument::Compact);
        if (data.size() > maxSelection) throw std::runtime_error("EINVAL: clipboard ownership exceeds 1 MiB");
        if (ftruncate(file.fd, 0) < 0) throw std::runtime_error(std::strerror(errno));
        qsizetype at = 0;
        while (at < data.size()) {
            const auto written = pwrite(file.fd, data.constData() + at, static_cast<size_t>(data.size() - at), at);
            if (written < 0 && errno == EINTR) continue;
            if (written <= 0) throw std::runtime_error("cannot save clipboard ownership");
            at += written;
        }
    }
};

QJsonArray filePaths(NSPasteboard *board) {
    QJsonArray paths;
    NSArray<NSURL *> *urls = [board readObjectsForClasses:@[[NSURL class]] options:@{NSPasteboardURLReadingFileURLsOnlyKey: @YES}];
    if (urls.count > maxPaths) throw std::runtime_error("EINVAL: clipboard file selection exceeds 10000 paths");
    for (NSURL *url in urls) {
        const auto path = qt(url.path);
        if (!url.isFileURL || (url.host.length && [url.host caseInsensitiveCompare:@"localhost"] != NSOrderedSame)
            || !path.startsWith('/') || path.contains(QChar::Null)) continue;
        paths.append(path);
    }
    if (QJsonDocument(paths).toJson(QJsonDocument::Compact).size() > maxSelection - 1024)
        throw std::runtime_error("EINVAL: clipboard file selection exceeds 1 MiB");
    return paths;
}

bool owns(NSPasteboard *board, const QJsonObject &state) {
    const auto token = state.value("marker").toString();
    return !token.isEmpty() && state.value("changeCount").toInteger(-1) == board.changeCount
        && token == qt([board stringForType:markerType]);
}

QJsonValue copyFiles(const QJsonObject &params, NSPasteboard *board, Ownership &ownership) {
    const auto paths = params.value("paths").toArray();
    if (paths.isEmpty()) throw std::runtime_error("EINVAL: clipboard.copy requires paths");
    if (paths.size() > maxPaths) throw std::runtime_error("EINVAL: clipboard file selection exceeds 10000 paths");
    if (QJsonDocument(paths).toJson(QJsonDocument::Compact).size() > maxSelection - 1024)
        throw std::runtime_error("EINVAL: clipboard file selection exceeds 1 MiB");
    NSString *token = NSUUID.UUID.UUIDString;
    NSMutableArray<NSPasteboardItem *> *items = [NSMutableArray array];
    for (const auto &value : paths) {
        const auto path = value.toString();
        if (!value.isString() || !path.startsWith('/') || path.contains(QChar::Null))
            throw std::runtime_error("clipboard.copy requires absolute paths");
        NSPasteboardItem *item = [[NSPasteboardItem alloc] init];
        NSURL *url = [NSURL fileURLWithPath:ns(path)];
        if (![item setString:url.absoluteString forType:NSPasteboardTypeFileURL])
            throw std::runtime_error("cannot encode clipboard file URL");
        if (items.count == 0 && ![item setString:token forType:markerType])
            throw std::runtime_error("cannot encode clipboard ownership");
        [items addObject:item];
    }
    [board clearContents];
    if (![board writeObjects:items]) {
        ownership.write({});
        throw std::runtime_error("cannot write clipboard files");
    }
    const bool cut = params.value("cut").toBool();
    ownership.write({{"marker", qt(token)}, {"changeCount", static_cast<qint64>(board.changeCount)}, {"cut", cut}, {"paths", paths}});
    return QJsonObject{{"paths", paths}, {"cut", cut}};
}

QJsonValue readFiles(NSPasteboard *board, const Ownership &ownership) {
    const auto count = board.changeCount;
    const auto paths = filePaths(board);
    const auto state = ownership.read();
    const bool mine = owns(board, state) && count == board.changeCount && paths == state.value("paths").toArray();
    return QJsonObject{{"paths", paths}, {"cut", mine && !paths.isEmpty() && state.value("cut").toBool()},
                       {"source", paths.isEmpty() ? QString() : mine ? QStringLiteral("dfiles") : QStringLiteral("text/uri-list")}};
}

QJsonValue readImage(NSPasteboard *board) {
    const auto count = board.changeCount;
    NSData *png = [board dataForType:NSPasteboardTypePNG];
    NSData *data = png ?: [board dataForType:NSPasteboardTypeTIFF];
    if (!data.length) return QJsonObject{};
    if (data.length > maxImage) throw std::runtime_error("EINVAL: clipboard image exceeds 32 MiB");
    CGImageSourceRef source = CGImageSourceCreateWithData((__bridge CFDataRef)data, nullptr);
    if (!source) return QJsonObject{};
    NSDictionary *properties = CFBridgingRelease(CGImageSourceCopyPropertiesAtIndex(source, 0, nullptr));
    CFRelease(source);
    const uint64_t width = [properties[(__bridge NSString *)kCGImagePropertyPixelWidth] unsignedLongLongValue];
    const uint64_t height = [properties[(__bridge NSString *)kCGImagePropertyPixelHeight] unsignedLongLongValue];
    if (!width || !height) return QJsonObject{};
    if (width > maxImagePixels || height > maxImagePixels / width)
        throw std::runtime_error("EINVAL: clipboard image exceeds 32 megapixels");
    if (!png) {
        NSBitmapImageRep *bitmap = [NSBitmapImageRep imageRepWithData:data];
        png = [bitmap representationUsingType:NSBitmapImageFileTypePNG properties:@{}];
    }
    if (!png.length || count != board.changeCount) return QJsonObject{};
    if (png.length > maxImage) throw std::runtime_error("EINVAL: clipboard image exceeds 32 MiB");
    const auto bytes = QByteArray::fromRawData(static_cast<const char *>(png.bytes), static_cast<qsizetype>(png.length));
    return QJsonObject{{"data", QString::fromLatin1(bytes.toBase64())}, {"mime", "image/png"}};
}
QJsonValue clearFiles(NSPasteboard *board, const Ownership &ownership) {
    const bool mine = owns(board, ownership.read());
    if (mine) [board clearContents];
    ownership.write({});
    return QJsonObject{{"cleared", mine}};
}
}

QJsonValue clipboard(const QString &method, const QJsonObject &params) {
    @autoreleasepool {
        NSPasteboard *board = NSPasteboard.generalPasteboard;
        if (method == "clipboard.image") return readImage(board);
        if (method != "clipboard.copy" && method != "clipboard.paths" && method != "clipboard.clear")
            throw std::runtime_error("unknown clipboard method");
        Ownership ownership;
        if (method == "clipboard.copy") return copyFiles(params, board, ownership);
        if (method == "clipboard.paths") return readFiles(board, ownership);
        return clearFiles(board, ownership);
    }
}
