#include "../runtime/shell.h"
#include "../runtime/io.h"

#include <QApplication>
#include <QClipboard>
#include <QElapsedTimer>
#include <QFile>
#include <QFileInfo>
#include <QMimeData>
#include <QQmlComponent>
#include <QQmlEngine>
#include <QQuickItem>
#include <QQuickImageProvider>
#include <QQuickWindow>
#include <QPainter>
#include <QPdfWriter>
#include <QTemporaryDir>
#include <QStyleHints>
#include <QKeySequence>
#include <QKeyEvent>
#include <QThread>
#include <QThreadPool>
#include <memory>

static void require(bool condition, const char *message) {
    if (!condition) qFatal("%s", message);
}
static QImage loadIcon(QQmlEngine &engine, const QString &id, QSize size, bool cancel = false) {
    auto provider = static_cast<QQuickAsyncImageProvider *>(engine.imageProvider(QStringLiteral("duxicons")));
    require(provider, "Native icon provider must be registered");
    std::unique_ptr<QQuickImageResponse> response(provider->requestImageResponse(id, size));
    bool finished = false;
    QObject::connect(response.get(), &QQuickImageResponse::finished, [&finished] { finished = true; });
    if (cancel) response->cancel();
    QElapsedTimer timer;
    timer.start();
    while (!finished && timer.elapsed() < 5000) { QCoreApplication::processEvents(); QThread::msleep(1); }
    require(finished, "Icon response must finish even when cancelled");
    if (cancel) return {};
    std::unique_ptr<QQuickTextureFactory> texture(response->textureFactory());
    return texture ? texture->image() : QImage();
}


static void checkFileClipboard(QQmlEngine &engine, const QString &shellRoot) {
    require(QGuiApplication::platformName() == QStringLiteral("offscreen"), "Clipboard tests require an isolated offscreen clipboard");
    auto clipboard = QGuiApplication::clipboard();
    QFile file(shellRoot + QStringLiteral("/Services/ClipboardService.qml"));
    require(file.open(QIODevice::ReadOnly), "ClipboardService source must load");
    auto source = file.readAll();
    source.replace("pragma Singleton\n", "");
    source.replace("import qs.Services\n", "");
    source.replace("property var transport: DuxFilesService", "property var transport: null");
    QQmlComponent component(&engine);
    component.setData(source, QUrl::fromLocalFile(file.fileName()));
    std::unique_ptr<QObject> service(component.create());
    if (!service) qFatal("%s", qPrintable(component.errorString()));
    QQmlComponent transportComponent(&engine);
    transportComponent.setData("import QtQuick; QtObject { property int calls: 0; function sendRequest() { calls++; } }", QUrl());
    std::unique_ptr<QObject> transport(transportComponent.create());
    require(transport != nullptr, "Clipboard transport fixture must load");
    service->setProperty("transport", QVariant::fromValue(transport.get()));
    auto api = engine.newQObject(engine.singletonInstance<QObject *>("Quickshell", "Quickshell"));
    const auto lookupIcon = [&](const QString &name, bool optional) {
        return api.property(QStringLiteral("iconPath")).callWithInstance(api, {name, QJSValue(optional)}).toString();
    };
    require(lookupIcon(QStringLiteral("folder-icon-that-no-theme-has"), true).isEmpty(), "Optional missing icons must let QML fall back to the base icon");
    require(!lookupIcon(QStringLiteral("folder-icon-that-no-theme-has"), false).isEmpty(), "Required icons must retain a generic native fallback");
    require(!lookupIcon(QStringLiteral("user-trash"), true).isEmpty(), "Optional Trash lookup must retain its native icon");
    for (const auto &name : {QStringLiteral("text-plain"), QStringLiteral("application-x-shellscript"), QStringLiteral("drive-harddisk")}) {
        const auto source = lookupIcon(name, true);
        require(source.startsWith(QStringLiteral("image://duxicons/")), "Optional file and drive icons must resolve to the native provider");
        require(!loadIcon(engine, name, QSize(32, 32)).isNull(), "Native file and drive icons must render");
    }
    auto ui = engine.newQObject(service.get());
    const auto call = [&ui](const char *method, const QJSValueList &args = QJSValueList{}) {
        const auto result = ui.property(QString::fromLatin1(method)).callWithInstance(ui, args);
        if (result.isError()) qFatal("%s", qPrintable(result.toString()));
    };
    const auto read = [&api] { return api.property(QStringLiteral("readFiles")).callWithInstance(api).toVariant().toMap(); };
    const auto readService = [&engine, &ui] {
        auto capture = engine.newObject();
        auto callback = engine.evaluate("(function(capture) { return function(result) { capture.result = result; }; })").call({capture});
        const auto result = ui.property(QStringLiteral("read")).callWithInstance(ui, {callback});
        require(!result.isError(), "ClipboardService read callback must run");
        return capture.property(QStringLiteral("result")).toVariant().toMap();
    };
    const QStringList paths{QStringLiteral("/tmp/dux clipboard/雪 #100%.txt"), QStringLiteral("/tmp/other.txt")};
    call("copy", {engine.toScriptValue(paths)});
    require(clipboard->mimeData()->urls() == QList<QUrl>{QUrl::fromLocalFile(paths[0]), QUrl::fromLocalFile(paths[1])}, "Copy must publish native file URLs without losing Unicode or escaping");
    require(readService().value(QStringLiteral("paths")).toStringList() == paths && !read().value(QStringLiteral("cut")).toBool(), "Copy must round trip file paths through the real QML service");
    require(service->property("hasFiles").toBool() && !service->property("cut").toBool(), "Copy must update menu and status state");
    call("cutPaths", {engine.toScriptValue(paths)});
    require(readService().value(QStringLiteral("cut")).toBool() && service->property("cut").toBool(), "Own cut selection must retain move semantics");
    require(read().value(QStringLiteral("source")).toString() == QStringLiteral("dfiles"), "Own selection must identify its source");
    call("copy", {engine.toScriptValue(QStringList{})});
    require(read().value(QStringLiteral("cut")).toBool(), "Empty copy must preserve the cut selection");
    const auto invalid = api.property(QStringLiteral("copyFiles")).callWithInstance(api, {engine.toScriptValue(QStringList{QStringLiteral("relative.txt")}), QJSValue(false)});
    require(!invalid.toBool() && read().value(QStringLiteral("cut")).toBool(), "Invalid copy must preserve the current clipboard");
    call("clear");
    require(!service->property("hasFiles").toBool() && (!clipboard->mimeData() || clipboard->mimeData()->urls().isEmpty()), "Clear must remove its own file selection");
    call("cutPaths", {engine.toScriptValue(paths)});
    auto foreign = new QMimeData;
    foreign->setUrls({QUrl::fromLocalFile(paths[0]), QUrl(QStringLiteral("https://example.invalid/remote")), QUrl::fromLocalFile(QStringLiteral("/tmp/Finder file.txt"))});
    foreign->setData(QStringLiteral("application/x-kde-cutselection"), "1");
    clipboard->setMimeData(foreign);
    call("refresh");
    const QStringList foreignPaths{paths[0], QStringLiteral("/tmp/Finder file.txt")};
    const auto external = readService();
    require(external.value(QStringLiteral("paths")).toStringList() == foreignPaths && !external.value(QStringLiteral("cut")).toBool(), "Foreign file URLs must copy and exclude nonlocal URLs");
    require(service->property("paths").toStringList() == foreignPaths && !service->property("cut").toBool(), "External replacement must update paste menus and remove cut dimming");
    require(clipboard->mimeData() == foreign, "Refresh must preserve foreign clipboard data");
    call("clear");
    require(clipboard->mimeData() == foreign, "Clear must preserve another application's file URLs");
    call("cutPaths", {engine.toScriptValue(paths)});
    auto same = new QMimeData;
    same->setUrls({QUrl::fromLocalFile(paths[0]), QUrl::fromLocalFile(paths[1])});
    clipboard->setMimeData(same);
    require(!readService().value(QStringLiteral("cut")).toBool(), "An identical external selection must never inherit own cut intent");
    call("clear");
    require(clipboard->mimeData() == same, "Clear must preserve an identical external selection");
    call("cutPaths", {engine.toScriptValue(paths)});
    clipboard->setText(QStringLiteral("external replacement"));
    call("clear");
    require(clipboard->text() == QStringLiteral("external replacement"), "Clear must preserve text copied after a file selection");
    call("refresh");
    require(!service->property("hasFiles").toBool() && !service->property("cut").toBool() && readService().value(QStringLiteral("paths")).toStringList().isEmpty(), "Text-only clipboard must clear local file indicators");
    require(transport->property("calls").toInt() == 0, "macOS file clipboard must bypass backend IPC");
    qInfo("Offscreen file clipboard checks passed");
}

static void checkMacShortcuts(QQmlEngine &engine, const QString &shellRoot) {
    engine.addImportPath(QFileInfo(shellRoot).absolutePath());
    QQmlComponent component(&engine);
    component.setData(R"QML(
import QtQuick
import qs.Common
Window {
    visible: true
    width: 200
    height: 120
    property string action: ""
    property var testEntries: Shortcuts.entries.filter(entry => ["trash", "nextTab", "showHidden"].includes(entry.id))
    Instantiator {
        model: testEntries
        Shortcut {
            required property var modelData
            sequences: modelData.keys
            onActivated: action = modelData.id
        }
    }
}
)QML", QUrl());
    std::unique_ptr<QObject> object(component.create());
    if (!object) qFatal("%s", qPrintable(component.errorString()));
    auto window = qobject_cast<QWindow *>(object.get());
    require(window, "Shortcut check must create a window");
    window->requestActivate();
    QCoreApplication::processEvents();
    auto key = [&](int code, Qt::KeyboardModifiers modifiers) {
        QKeyEvent override(QEvent::ShortcutOverride, code, modifiers);
        QCoreApplication::sendEvent(window, &override);
        QKeyEvent press(QEvent::KeyPress, code, modifiers);
        QCoreApplication::sendEvent(window, &press);
        QKeyEvent release(QEvent::KeyRelease, code, modifiers);
        QCoreApplication::sendEvent(window, &release);
        QCoreApplication::processEvents();
    };
    key(Qt::Key_Backspace, Qt::ControlModifier);
    require(object->property("action") == QStringLiteral("trash"), "Command+Delete must invoke Trash");
    key(Qt::Key_Tab, Qt::ControlModifier);
    require(object->property("action") == QStringLiteral("trash"), "Command+Tab must remain reserved for macOS");
    key(Qt::Key_Tab, Qt::MetaModifier);
    require(object->property("action") == QStringLiteral("nextTab"), "Physical Control+Tab must change tabs");
    key(Qt::Key_Period, Qt::ControlModifier | Qt::ShiftModifier);
    require(object->property("action") == QStringLiteral("showHidden"), "Command+Shift+period must toggle hidden files");
}

static void checkMedia(QQmlEngine &engine, const QString &shellRoot) {
    require(QKeySequence(QStringLiteral("Ctrl+C")).toString(QKeySequence::NativeText).contains(QChar(0x2318)), "Existing Ctrl shortcuts must display the native Command modifier on macOS");
    require(!DuxCocoa::fileIcon(QStringLiteral("user-trash"), QSize(32, 32)).isNull(), "Trash must use a native trash icon");
    require(!DuxCocoa::fileIcon(QStringLiteral("user-trash-full"), QSize(32, 32)).isNull(), "Full Trash must have a native icon");
    QTemporaryDir temporary;
    require(temporary.isValid(), "Media fixtures need a temporary directory");
    const auto path = temporary.filePath(QStringLiteral("雪 #100%.png"));
    QImage original(200, 100, QImage::Format_RGB32);
    original.fill(Qt::blue);
    {
        QPainter painter(&original);
        painter.fillRect(0, 0, 200, 50, Qt::red);
    }
    require(original.save(path), "Image fixture must save");
    const auto thumbnail = DuxCocoa::thumbnail(QUrl::fromLocalFile(path), QSize(80, 80));
    require(thumbnail.size() == QSize(80, 40), "ImageIO thumbnails must preserve aspect ratio and requested bounds");
    require(thumbnail.pixelColor(40, 5).red() > 240 && thumbnail.pixelColor(40, 35).blue() > 240, "ImageIO must preserve image orientation and colors");
    const auto quickLook = DuxCocoa::quickLookThumbnail(QUrl::fromLocalFile(path), QSize(80, 80));
    require(!quickLook.isNull() && quickLook.width() <= 80 && quickLook.height() <= 80, "Quick Look must return a bounded real thumbnail for local media");
    const auto transparentPath = temporary.filePath(QStringLiteral("transparent.png"));
    QImage transparent(64, 64, QImage::Format_RGBA8888_Premultiplied);
    transparent.fill(Qt::transparent);
    {
        QPainter painter(&transparent);
        painter.fillRect(16, 16, 32, 32, QColor(255, 0, 0, 128));
    }
    require(transparent.save(transparentPath), "Transparent image fixture must save");
    const auto transparentThumbnail = DuxCocoa::thumbnail(QUrl::fromLocalFile(transparentPath), QSize(64, 64));
    require(!transparentThumbnail.isNull() && transparentThumbnail.pixelColor(0, 0).alpha() == 0,
        "Transparent pixels must not blend with uninitialized backing memory");
    const auto translucent = transparentThumbnail.pixelColor(32, 32);
    require(translucent.alpha() == 128 && translucent.red() == 255 && translucent.green() == 0 && translucent.blue() == 0,
        "Native image conversion must preserve translucent colors and alpha");

    const auto pdfPath = temporary.filePath(QStringLiteral("pages #雪.pdf"));
    {
        QPdfWriter writer(pdfPath);
        writer.setResolution(72);
        writer.setPageSize(QPageSize(QSizeF(200, 100), QPageSize::Point));
        writer.setPageMargins(QMarginsF(0, 0, 0, 0));
        QPainter painter(&writer);
        painter.drawImage(QPoint(0, 0), original);
        require(writer.newPage(), "PDF fixture must contain a second page");
        painter.fillRect(0, 0, 200, 100, Qt::green);
    }
    const auto pdf = QUrl::fromLocalFile(pdfPath);
    const auto pages = DuxCocoa::pdfPages(pdf);
    require(pages.size() == 2 && pages[0].toMap().value(QStringLiteral("width")).toInt() == 200, "Native PDF must report real page count and dimensions");
    const auto first = DuxCocoa::renderPdfPage(pdf, 0, 0, QSize(100, 100));
    require(first.size() == QSize(100, 50), "PDF rendering must fit the requested size");
    require(first.pixelColor(50, 5).red() > 240 && first.pixelColor(50, 45).blue() > 240, "PDF pages must render upright with actual page content");
    const auto second = DuxCocoa::renderPdfPage(pdf, 1, 0, QSize(100, 100));
    require(second.pixelColor(50, 25).green() > 240 && second.pixelColor(50, 25).red() < 20, "PDF page selection must render the requested page");
    const auto rotated = DuxCocoa::renderPdfPage(pdf, 0, 90, QSize(100, 100));
    require(rotated.size() == QSize(50, 100) && rotated == DuxCocoa::renderPdfPage(pdf, 0, -270, QSize(100, 100)), "PDF rotation must swap dimensions and normalize negative angles");
    require(rotated.pixelColor(5, 50).blue() > 240 && rotated.pixelColor(45, 50).red() > 240, "PDF rotation must turn page content clockwise");
    require(DuxCocoa::thumbnail(pdf, QSize(100, 100)) == first, "PDF thumbnails must show the first page");
    require(DuxCocoa::renderPdfPage(pdf, 2, 0, QSize(100, 100)).isNull(), "Out-of-range PDF pages must fail safely");
    require(DuxCocoa::pdfPages(QUrl::fromLocalFile(path)).isEmpty(), "Non-PDF input must fail PDF loading");
    require(DuxCocoa::thumbnail(QUrl(QStringLiteral("https://example.invalid/image.png")), QSize(100, 100)).isNull(), "Native media must reject remote URLs");
    require(DuxCocoa::thumbnail(QUrl::fromLocalFile(QStringLiteral("relative.png")), QSize(100, 100)).isNull(), "Native media must reject relative file URLs");

    auto nativePlatform = engine.singletonInstance<QObject *>("Quickshell", "NativePlatform");
    auto platform = engine.newQObject(nativePlatform);
    const auto packagePath = temporary.filePath(QStringLiteral("Example.app"));
    const auto folderPath = temporary.filePath(QStringLiteral("folder"));
    require(QDir().mkpath(packagePath) && QDir().mkpath(folderPath), "Package fixtures must be created");
    require(platform.property(QStringLiteral("isPackage")).callWithInstance(platform, {packagePath}).toBool(), "NativePlatform must identify a macOS package");
    require(!platform.property(QStringLiteral("isPackage")).callWithInstance(platform, {folderPath}).toBool(), "NativePlatform must not identify an ordinary directory as a package");
    require(platform.property(QStringLiteral("localContent")).callWithInstance(platform, {path}).toBool(), "Local media must be eligible for preview");
    require(platform.property(QStringLiteral("localContent")).callWithInstance(platform, {temporary.filePath(QStringLiteral("missing.pdf"))}).toBool(), "Missing local media must reach the preview error path");
    require(!platform.property(QStringLiteral("localContent")).callWithInstance(platform, {QStringLiteral("relative.pdf")}).toBool(), "Relative paths must not be eligible for preview");
    const auto nativeScheme = QGuiApplication::styleHints()->colorScheme();
    const auto expectedScheme = nativeScheme == Qt::ColorScheme::Dark ? 1 : nativeScheme == Qt::ColorScheme::Light ? 2 : 0;
    require(nativePlatform->property("colorScheme").toInt() == expectedScheme && nativePlatform->property("available").toBool() == (expectedScheme != 0), "Native appearance must expose the platform color scheme including unavailable offscreen state");
    const auto url = platform.property(QStringLiteral("thumbnailUrl")).callWithInstance(platform, {path}).toString();
    QQmlComponent component(&engine);
    component.setData(R"QML(
import QtQuick
import Quickshell
Item {
    property alias source: document.source
    property alias status: document.status
    property alias pageCount: document.pageCount
    property alias thumbnail: thumbnail.source
    NativePdfDocument { id: document }
    Image { objectName: "thumbnail"; id: thumbnail; sourceSize: Qt.size(80, 80) }
    Image { objectName: "pdfPage"; source: document.status === NativePdfDocument.Ready ? document.pageSource(1, 90) : ""; sourceSize: Qt.size(80, 80) }
}
)QML", QUrl());
    std::unique_ptr<QObject> media(component.create());
    if (!media) qFatal("%s", qPrintable(component.errorString()));
    media->setProperty("source", pdf);
    media->setProperty("thumbnail", QUrl(url));
    auto thumbnailItem = media->findChild<QObject *>(QStringLiteral("thumbnail"));
    auto pageItem = media->findChild<QObject *>(QStringLiteral("pdfPage"));
    QElapsedTimer timer;
    timer.start();
    while ((media->property("status").toInt() == 1 || thumbnailItem->property("status").toInt() != 1 || pageItem->property("status").toInt() != 1) && timer.elapsed() < 10000) {
        QCoreApplication::processEvents();
        QThread::msleep(1);
    }
    require(media->property("status").toInt() == 2 && media->property("pageCount").toInt() == 2, "NativePdfDocument must load asynchronously through QML");
    require(thumbnailItem->property("status").toInt() == 1 && pageItem->property("status").toInt() == 1, "duxmedia provider must load real images and PDF pages with escaped local paths");
    media->setProperty("source", QUrl::fromLocalFile(temporary.filePath(QStringLiteral("missing.pdf"))));
    timer.restart();
    while (media->property("status").toInt() == 1 && timer.elapsed() < 10000) { QCoreApplication::processEvents(); QThread::msleep(1); }
    require(media->property("status").toInt() == 3 && media->property("pageCount").toInt() == 0, "Missing PDF input must clear previous pages and report failure");
    media->setProperty("source", pdf);
    media->setProperty("source", QUrl());
    timer.restart();
    while (timer.elapsed() < 100) { QCoreApplication::processEvents(); QThread::msleep(1); }
    require(media->property("status").toInt() == 0 && media->property("pageCount").toInt() == 0, "Stale PDF work must not restore pages after closing");

    engine.addImportPath(QFileInfo(shellRoot).absolutePath());
    QQmlComponent pathsComponent(&engine);
    pathsComponent.setData("import QtQuick; import qs.DCommon.FileBrowser; QtObject { function resolve(path) { return FilePaths.toFileUrl(path); } }", QUrl());
    std::unique_ptr<QObject> pathsObject(pathsComponent.create());
    if (!pathsObject) qFatal("%s", qPrintable(pathsComponent.errorString()));
    auto pathsApi = engine.newQObject(pathsObject.get());
    require(pathsApi.property(QStringLiteral("resolve")).callWithInstance(pathsApi, {url}).toString() == url, "FilePaths must preserve native media URLs for image consumers");
    QQmlComponent preview(&engine, QUrl::fromLocalFile(shellRoot + QStringLiteral("/Modules/PreviewPdf.qml")));
    std::unique_ptr<QObject> view(preview.create());
    if (!view) qFatal("%s", qPrintable(preview.errorString()));
    view->setProperty("width", 400);
    view->setProperty("height", 300);
    view->setProperty("source", pdf);
    timer.restart();
    while (!view->property("ready").toBool() && timer.elapsed() < 10000) { QCoreApplication::processEvents(); QThread::msleep(1); }
    require(view->property("ready").toBool() && view->property("pageCount").toInt() == 2, "Staged PDF preview must load native pages without Qt PDF");
    require(qFuzzyCompare(view->property("fitScale").toDouble(), 2.0), "PDF preview must initially fit the page to the viewport");
    view->setProperty("zoom", 2);
    require(qFuzzyCompare(view->property("renderScale").toDouble(), 4.0), "PDF preview zoom must scale rendered pages");
    view->setProperty("angle", 90);
    require(qFuzzyCompare(view->property("fitScale").toDouble(), 1.5), "PDF preview rotation must recompute page fit");
    auto list = view->findChild<QQuickItem *>(QStringLiteral("nativePdfPages"));
    require(list, "PDF preview must expose a scrollable page list");
    auto scriptView = engine.newQObject(view.get());
    QMetaObject::invokeMethod(list, "forceLayout");
    scriptView.property(QStringLiteral("scrollPage")).callWithInstance(scriptView, {1});
    require(list->property("contentY").toDouble() > 0, "PDF page scrolling must move the viewport");
    scriptView.property(QStringLiteral("goToPage")).callWithInstance(scriptView, {0});
    require(qAbs(list->property("contentY").toDouble() - list->property("originY").toDouble()) < 1, "PDF page navigation must return to the requested page");
    const auto paths = platform.property(QStringLiteral("localPaths")).callWithInstance(platform, {engine.toScriptValue(QList<QUrl>{QUrl::fromLocalFile(path), QUrl(QStringLiteral("file://remote.example/file")), QUrl(QStringLiteral("https://example.invalid/remote")), QUrl::fromLocalFile(QStringLiteral("relative"))})}).toVariant().toStringList();
    require(paths == QStringList{path}, "Native drops must preserve escaped local paths and reject remote or relative URLs");
    qInfo("Native media checks passed");
    view.reset();
    media.reset();
    QThreadPool::globalInstance()->waitForDone();
    QCoreApplication::processEvents();
}

static void checkIcons(QQmlEngine &engine) {
    QQmlComponent component(&engine);
    component.setData(R"QML(
import QtQuick
import Quickshell
Item {
    property string missing: Quickshell.iconPath("not_a_real_material_glyph", true)
    property string compressed: Quickshell.iconPath("application-x-bzip2", true)
    property string pdf: Quickshell.iconPath("application-pdf", true)
    Image { objectName: "compressed"; source: parent.compressed; asynchronous: false; sourceSize: Qt.size(64, 64) }
    Image { objectName: "pdf"; source: parent.pdf; asynchronous: false; sourceSize: Qt.size(64, 64) }
}
)QML", QUrl());
    std::unique_ptr<QObject> object(component.create());
    if (!object) qFatal("%s", qPrintable(component.errorString()));
    require(object->property("missing").toString().isEmpty(), "Unknown icons must retain the glyph fallback");
    QElapsedTimer timer;
    timer.start();
    for (const auto name : {"compressed", "pdf"}) {
        auto image = object->findChild<QObject *>(QString::fromLatin1(name));
        require(image, "MIME icon image must exist");
        while (image->property("status").toInt() == 2 && timer.elapsed() < 5000) { QCoreApplication::processEvents(); QThread::msleep(1); }
        require(image->property("status").toInt() == 1, "Known MIME icons must load through QML");
    }
    const auto compressed = loadIcon(engine, QStringLiteral("application-x-bzip2"), QSize(64, 64));
    const auto pdf = loadIcon(engine, QStringLiteral("application-pdf"), QSize(64, 64));
    loadIcon(engine, QStringLiteral("text-plain"), QSize(64, 64), true);
    const auto generic = DuxCocoa::fileIcon(QStringLiteral("not-a-real-mime"), QSize(64, 64));
    require(!compressed.isNull() && !pdf.isNull() && compressed != generic && compressed != pdf, "Distinct MIME types must render their native icons");
    QTemporaryDir dir;
    require(dir.isValid(), "Icon path test directory must exist");
    const auto path = dir.filePath(QStringLiteral("space # percent % icon.png"));
    QImage contents(200, 100, QImage::Format_RGB32);
    contents.fill(Qt::red);
    require(contents.save(path), "Icon path test image must be created");
    const auto escaped = QString::fromLatin1(QUrl::toPercentEncoding(path));
    const auto fromProvider = loadIcon(engine, escaped, QSize(64, 64));
    require(fromProvider.size() == QSize(64, 64), "File paths must load native square icons, not the file's image contents");
}

int main(int argc, char **argv) {
    const bool clipboardOnly = argc > 2 && QString::fromLocal8Bit(argv[2]) == QStringLiteral("--clipboard-test");
    const bool mediaOnly = argc > 2 && QString::fromLocal8Bit(argv[2]) == QStringLiteral("--media-test");
    if (clipboardOnly || mediaOnly) qputenv("QT_QPA_PLATFORM", "offscreen");
    if (mediaOnly) qunsetenv("DFILES_SOCKET");
    QApplication app(argc, argv);
    app.setQuitOnLastWindowClosed(false);
    QQmlEngine engine;
    const QString shellRoot = argc > 1 ? QString::fromLocal8Bit(argv[1]) : QStringLiteral("quickshell");
    registerShellTypes(&engine, shellRoot);
    if (!clipboardOnly && !mediaOnly) checkIcons(engine);
    registerIoTypes();
    if (!mediaOnly && QGuiApplication::platformName() == QStringLiteral("offscreen")) checkFileClipboard(engine, shellRoot);
    if (clipboardOnly) return 0;
    if (mediaOnly) {
        checkMacShortcuts(engine, shellRoot);
        checkMedia(engine, shellRoot);
        return 0;
    }
    QQmlComponent component(&engine);
    component.setData(R"QML(
import QtQuick
import Quickshell
import Quickshell.Widgets
import Quickshell.Wayland
ShellRoot {
    id: root
    property bool loaderReady: false
    property bool closeReported: false
    readonly property string clipboard: Quickshell.clipboardText
    readonly property var activeToplevel: ToplevelManager.activeToplevel
    property bool nativeControls: false
    function adjustPopup() { popup.anchor.margins.top = 7; }
    property int mappedX: -1
    property int mappedY: -1
    property string resolvedFolder: Quickshell.iconPath("folder", true)
    Scope {
        objectName: "scope"
        property int top: 7
        property bool enabled: true
        Timer { interval: 1 }
        Item { objectName: "shelf" }
    }
    Singleton {
        objectName: "singleton"
        property int top: 11
        readonly property bool enabled: true
        Timer { interval: 1 }
    }
    property var options: ["one", "two"]
    ScriptModel {
        id: scriptModel
        objectName: "scriptModel"
        values: root.options
    }
    Instantiator {
        id: repeatOptions
        objectName: "repeater"
        model: scriptModel
        Item {
            required property string modelData
            objectName: "option-" + modelData
        }
    }
    function reorderOptions() { options = ["two", "three", "one"]; }
    function removeOptions() { options = ["one"]; }
    function replaceOptions() { options = []; }
    readonly property int nativeWindows: ToplevelManager.toplevels.values.length
    FloatingWindow {
        id: window
        objectName: "window"
        implicitWidth: 320
        implicitHeight: 240
        minimumSize: Qt.size(120, 80)
        maximumSize: Qt.size(1000, 900)
        surfaceFormat.opaque: false
        color: "transparent"
        visible: false
        onClosed: closeReported = true
        Item {
            id: child
            objectName: "child"
            x: 17
            y: 23
            width: 40
            height: 40
            Component.onCompleted: {
                const pos = QsWindow.itemPosition(child);
                mappedX = pos.x;
                mappedY = pos.y;
            }
        }
        BackgroundEffect.blurRegion: Region {
            width: 320
            height: 240
            radius: 12
            Region {
                intersection: Intersection.Intersect
                width: 160
                height: 240
            }
        }
        ClippingRectangle {
            objectName: "clip"
            x: 8
            y: 8
            width: 80
            height: 80
            radius: 20
            topLeftRadius: 10
            topRightRadius: 0
            color: "red"
            Rectangle { anchors.fill: parent; color: "blue" }
        }
        IconImage {
            objectName: "icon"
            x: 110
            implicitSize: 32
            source: Quickshell.iconPath("folder", true)
            backer.sourceSize: Qt.size(64, 64)
            asynchronous: false
            mipmap: true
        }
    }
    LazyLoader {
        id: lazy
        active: false
        FloatingWindow {
            objectName: "modal"
            parentWindow: window
            property int answer: 42
            property bool opened: false
            function open() { opened = true; }
        }
    }
    PopupWindow {
        id: popup
        objectName: "popup"
        anchor.window: window
        anchor.rect.x: 5
        anchor.rect.y: 9
        anchor.edges: Edges.Top | Edges.Left
        anchor.gravity: Edges.Bottom | Edges.Right
        anchor.margins.top: 0
        anchor.adjustment: PopupAdjustment.None
        implicitWidth: 90
        implicitHeight: 80
        grabFocus: false
    }
    Component.onCompleted: {
        lazy.active = true;
        lazy.item.open();
        loaderReady = lazy.item.answer === 42 && lazy.item.opened;
        nativeControls = typeof window.startSystemMove === "function" && typeof window.startSystemResize === "function";
        if (Qt.platform.pluginName === "offscreen")
            Quickshell.clipboardText = "dux-shell-test";
    }
}
)QML", QUrl::fromLocalFile(shellRoot + QStringLiteral("/compat-test.qml")));
    QElapsedTimer timer;
    timer.start();
    while (component.isLoading() && timer.elapsed() < 10000) {
        app.processEvents();
        QThread::msleep(1);
    }
    if (component.isError()) qFatal("%s", qPrintable(component.errorString()));
    std::unique_ptr<QObject> root(component.create());
    if (!root) qFatal("%s", qPrintable(component.errorString()));
    app.processEvents();
    auto scope = root->findChild<QObject *>(QStringLiteral("scope"));
    auto singleton = root->findChild<QObject *>(QStringLiteral("singleton"));
    require(scope && scope->property("top").toInt() == 7 && singleton && singleton->property("top").toInt() == 11, "Scope and Singleton must allow original service properties");
    require(scope->findChild<QObject *>(QStringLiteral("shelf")), "Nonvisual scope must retain visual children for icon pooling");
    const auto option = [&root](const QString &name) -> QObject * { return root->findChild<QObject *>(name); };
    auto one = option(QStringLiteral("option-one"));
    auto two = option(QStringLiteral("option-two"));
    require(one && two, "ScriptModel must expose modelData to original delegates");
    require(QMetaObject::invokeMethod(root.get(), "reorderOptions"), "ScriptModel input must update");
    require(option(QStringLiteral("option-one")) == one && option(QStringLiteral("option-two")) == two, "ScriptModel reorder must preserve delegate identity");
    require(option(QStringLiteral("option-three")), "ScriptModel must insert new values");
    require(QMetaObject::invokeMethod(root.get(), "removeOptions"), "ScriptModel removal input must update");
    auto model = root->findChild<QObject *>(QStringLiteral("scriptModel"));
    require(model && model->property("count").toInt() == 1 && option(QStringLiteral("option-one")) == one, "ScriptModel must remove rows while preserving retained delegates");
    require(QMetaObject::invokeMethod(root.get(), "replaceOptions"), "ScriptModel clear input must update");
    require(model->property("count").toInt() == 0, "ScriptModel empty input must clear delegates");
    require(root->property("loaderReady").toBool(), "LazyLoader must construct its item synchronously");
    auto window = root->findChild<QQuickWindow *>(QStringLiteral("window"));
    require(window != nullptr, "FloatingWindow must be a QQuickWindow");
    require(window->size() == QSize(320, 240), "Implicit dimensions must size native window");
    require(window->minimumSize() == QSize(120, 80), "Minimum size must reach native window");
    require(window->maximumSize() == QSize(1000, 900), "Maximum size must reach native window");
    auto modal = root->findChild<QQuickWindow *>(QStringLiteral("modal"));
    require(modal && modal->transientParent() == window, "FileBrowserModal parentWindow must bridge to native transientParent");
    auto child = root->findChild<QQuickItem *>(QStringLiteral("child"));
    require(child && child->parentItem() == window->contentItem(), "Window default children must attach to contentItem");
    require(root->property("mappedX").toInt() == 17 && root->property("mappedY").toInt() == 23, "QsWindow must map actual item coordinates");
    auto icon = root->findChild<QQuickItem *>(QStringLiteral("icon"));
    require(icon && icon->implicitWidth() == 32 && icon->implicitHeight() == 32, "Icon implicitSize must be preserved");
    require(icon->property("status").toInt() == 1, "Bundled SVG icon must load");
    auto clip = root->findChild<QQuickItem *>(QStringLiteral("clip"));
    require(clip && clip->childItems().size() >= 2, "Rounded clip must preserve original content");
    auto popup = root->findChild<QQuickWindow *>(QStringLiteral("popup"));
    require(popup && popup->transientParent() == window, "Popup anchor must use native transient parent");
    require(popup->position() == window->position() + QPoint(5, 9), "Popup anchor must map host coordinates");
    window->show();
    timer.restart();
    while ((!window->isExposed() || root->property("nativeWindows").toInt() == 0) && timer.elapsed() < 5000) {
        app.processEvents();
        QThread::msleep(1);
    }
    require(root->property("nativeWindows").toInt() == 1, "Native toplevel list must include visible floating windows only");
    const auto image = window->grabWindow();
    require(!image.isNull(), "Native Qt Quick window must render");
    const auto pixel = [&image](int x, int y) {
        return image.pixelColor(qRound(x * image.devicePixelRatio()), qRound(y * image.devicePixelRatio()));
    };
    require(pixel(8, 8).alpha() < 20, "Rounded clip must remove child pixels outside the radius");
    require(pixel(86, 9).blue() > 200, "Per-corner radius must preserve square corners");
    const auto center = pixel(48, 48);
    require(center.blue() > 200 && center.red() < 20, "Rounded clip must preserve child pixels in the center");
    if (argc > 2) require(image.save(QString::fromLocal8Bit(argv[2])), "Clip evidence image must save");
    if (QGuiApplication::platformName() == QStringLiteral("offscreen"))
        require(root->property("clipboard").toString() == QStringLiteral("dux-shell-test"), "Clipboard must reach the isolated Qt clipboard");
    require(root->property("nativeControls").toBool(), "Dux move and resize controls must be callable");
    require(QMetaObject::invokeMethod(root.get(), "adjustPopup"), "Popup margin update must be callable");
    require(popup->position() == window->position() + QPoint(5, 16), "Grouped margin changes must update popup position");
    if (QGuiApplication::platformName() == QStringLiteral("cocoa")) {
        DuxCocoa::activateWindow(window);
        timer.restart();
        while (!window->isActive() && timer.elapsed() < 5000) { app.processEvents(); QThread::msleep(1); }
        require(window->isActive(), "Cocoa window must activate natively");
        auto active = root->property("activeToplevel").value<QObject *>();
        require(active && active->property("appId").toString() == QStringLiteral("dev.duxlabs.files"), "ToplevelManager must expose native focus");
        require(QMetaObject::invokeMethod(active, "activate"), "Toplevel activation must be callable");
    }
    window->setProperty("maximized", true);
    require(window->windowState() == Qt::WindowMaximized, "Maximize must reach native state");
    window->setProperty("minimized", true);
    require(window->windowState() == Qt::WindowMinimized, "Minimize must reach native state");
    window->setProperty("minimized", false);
    require(!window->property("minimized").toBool(), "Restore must leave minimized state");
    require(!window->close(), "FloatingWindow must let QML decide close outcome");
    require(root->property("closeReported").toBool(), "Window close must emit closed");
    qInfo("Shell compatibility checks passed");
    return 0;
}
