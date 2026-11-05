#include "io.h"

#include <QCoreApplication>
#include <QDir>
#include <QEventLoop>
#include <QFile>
#include <QFileInfo>
#include <QJsonDocument>
#include <QJsonObject>
#include <QLocalServer>
#include <QLocalSocket>
#include <QMetaProperty>
#include <QPointer>
#include <QProcess>
#include <QQmlComponent>
#include <QQmlContext>
#include <QQmlEngine>
#include <QSaveFile>
#include <QTemporaryDir>
#include <QTimer>
#include <QUrl>
#include <cstdlib>
#include <memory>

static void check(bool passed, const char *message) {
    if (passed) return;
    qFatal("%s", message);
}

static void put(const QString &path, const QByteArray &bytes) {
    QSaveFile file(path);
    check(file.open(QIODevice::WriteOnly), "open test file");
    check(file.write(bytes) == bytes.size(), "write test file");
    check(file.commit(), "commit test file");
}

static QByteArray contents(const QString &path) {
    QFile file(path);
    check(file.open(QIODevice::ReadOnly), "open saved file");
    return file.readAll();
}

static std::unique_ptr<QObject> create(QQmlEngine &engine, const QByteArray &qml) {
    QQmlComponent component(&engine);
    component.setData(qml, QUrl("file:///io-test.qml"));
    if (component.isError()) qCritical() << component.errors();
    check(component.isReady(), "QML component is ready");
    auto object = std::unique_ptr<QObject>(component.create());
    if (!object) qCritical() << component.errors();
    check(bool(object), "QML object created");
    return object;
}

static bool waitFor(QObject *object, const char *name, int timeout = 5000) {
    const auto property = object->metaObject()->property(object->metaObject()->indexOfProperty(name));
    check(property.isValid() && property.hasNotifySignal(), "waitable QML property");
    if (property.read(object).toBool()) return true;
    QEventLoop loop;
    QTimer deadline;
    deadline.setSingleShot(true);
    const auto quit = loop.metaObject()->method(loop.metaObject()->indexOfSlot("quit()"));
    QObject::connect(object, property.notifySignal(), &loop, quit);
    QObject::connect(&deadline, &QTimer::timeout, &loop, &QEventLoop::quit);
    deadline.start(timeout);
    while (!property.read(object).toBool() && deadline.isActive()) loop.exec();
    return property.read(object).toBool();
}

int main(int argc, char **argv) {
    QCoreApplication app(argc, argv);
    registerIoTypes();
    QTemporaryDir dir;
    check(dir.isValid(), "temporary directory");
    QQmlEngine engine;
    const auto path = dir.filePath("settings with spaces.json");
    engine.rootContext()->setContextProperty("testPath", QUrl::fromLocalFile(path).toString());
    engine.rootContext()->setContextProperty("missingPath", dir.filePath("missing/a/b/settings.json"));
    engine.rootContext()->setContextProperty("testDirectory", dir.path());
    put(path, R"({"density":"compact","radius":72,"extra":100,"values":{"nested":[1,2]}})");
    {
        auto object = create(engine, R"(
import QtQml
import Quickshell.Io 1.0
FileView {
    id: root
    path: testPath
    blockLoading: true
    blockWrites: true
    printErrors: false
    property int loadCount: 0
    property int changes: 0
    property bool savedOk: false
    property alias density: settings.density
    property alias radius: settings.radius
    property alias font: settings.font
    property alias values: settings.values
    onLoaded: loadCount++
    onSaved: savedOk = true
    onAdapterUpdated: changes++
    JsonAdapter {
        id: settings
        property string density: "comfortable"
        property int radius: 50
        property string font: "Bundled Sans"
        property var values: ({})
    }
    function persist() { writeAdapter(); }
})");
        check(object->property("density") == "compact", "blocking adapter load before first frame");
        check(object->property("radius") == 72, "integer adapter load");
        check(object->property("font") == "Bundled Sans", "missing JSON key preserves QML defaults");
        check(object->property("loadCount") == 1, "loaded signal exactly once for blocking load");
        check(object->property("changes") == 0, "disk load does not trigger adapter persistence");
        object->setProperty("density", "comfortable");
        check(object->property("changes") == 1, "QML dynamic property change notification");
        check(QMetaObject::invokeMethod(object.get(), "persist"), "adapter persistence invocation");
        check(object->property("savedOk").toBool(), "blocking adapter write completed");
        const auto json = QJsonDocument::fromJson(contents(path)).object();
        check(json["density"] == "comfortable" && json["font"] == "Bundled Sans", "serialized dynamic adapter properties");
        check(json["values"].toObject()["nested"].isArray(), "nested JSON var round trip");
        check(!json.contains("objectName") && !json.contains("extra"), "only declared adapter properties persisted");
    }
    {
        auto object = create(engine, R"(
import QtQml
import Quickshell.Io 1.0
FileView {
    path: testPath
    preload: false
    blockLoading: true
    property bool done: false
    property int loads: 0
    onLoaded: { loads++; done = true; }
    function readNow() { return text(); }
})");
        check(object->property("loads") == 0, "preload false defers disk access");
        QVariant text;
        check(QMetaObject::invokeMethod(object.get(), "readNow", Q_RETURN_ARG(QVariant, text)), "deferred read callable");
        check(text.toString().contains("comfortable"), "deferred read returns persisted bytes");
        check(object->property("done").toBool(), "deferred blocking text load");
    }
    {
        auto object = create(engine, R"(
import QtQml
import Quickshell.Io 1.0
FileView {
    path: missingPath
    blockLoading: true
    blockWrites: true
    watchChanges: true
    printErrors: false
    property bool done: false
    property int errorCode: -1
    onLoadFailed: error => errorCode = error
    onSaved: done = true
    Component.onCompleted: { text(); setText("created"); }
})");
        check(object->property("errorCode") == 2, "missing file reports FileNotFound");
        check(object->property("done").toBool(), "write creates parent directories");
        check(contents(dir.filePath("missing/a/b/settings.json")) == "created", "atomic write bytes match");
    }
    {
        auto object = create(engine, R"(
import QtQml
import Quickshell.Io 1.0
FileView {
    id: root
    path: testPath
    watchChanges: true
    blockLoading: true
    property bool done: false
    property string current: ""
    property int loads: 0
    onLoaded: { current = text(); loads++; if (loads > 1) done = true; }
    onFileChanged: reload()
    Component.onCompleted: text()
})");
        put(path, "external atomic replacement");
        check(waitFor(object.get(), "done"), "event-driven file watcher reloads atomic replacement");
        check(object->property("current") == "external atomic replacement", "watcher read fresh content");
        object->setProperty("done", false);
        put(path, "second atomic replacement");
        check(waitFor(object.get(), "done"), "file watcher rearmed after replacement");
        check(object->property("current") == "second atomic replacement", "second watcher read fresh content");
    }
    {
        auto object = create(engine, R"(
import QtQml
import Quickshell.Io 1.0
FileView {
    path: testDirectory
    printErrors: false
    property bool done: false
    property int code: -1
    onLoadFailed: error => { code = error; done = true; }
})");
        check(waitFor(object.get(), "done"), "directory read failure emitted");
        check(object->property("code") == 4, "directory reports NotAFile");
    }
    {
        auto object = create(engine, R"(
import QtQml
import Quickshell.Io 1.0
Process {
    command: ["/bin/sh", "-c", "printf 'hello'; printf 'failure' >&2; exit 7"]
    running: true
    property bool done: false
    property bool outDone: false
    property bool errDone: false
    property string capturedOut: ""
    property string capturedErr: ""
    property int code: 0
    stdout: StdioCollector { onStreamFinished: { capturedOut = text; outDone = true; } }
    stderr: StdioCollector { onStreamFinished: { capturedErr = text; errDone = true; } }
    onExited: exitCode => { code = exitCode; done = outDone && errDone; }
})");
        check(waitFor(object.get(), "done"), "process collectors finish before exited");
        check(object->property("capturedOut") == "hello", "stdout collected");
        check(object->property("capturedErr") == "failure", "stderr collected");
        check(object->property("code") == 7, "child exit code retained");
        check(!object->property("running").toBool(), "process running state cleared");
    }
    {
        auto object = create(engine, R"(
import QtQml
import Quickshell.Io 1.0
Process {
    command: ["/no/such/dux-files-executable"]
    running: true
    property bool done: false
    property int code: 0
    property int streams: 0
    stdout: StdioCollector { onStreamFinished: streams++ }
    stderr: StdioCollector { onStreamFinished: streams++ }
    onExited: exitCode => { code = exitCode; done = streams === 2; }
})");
        check(waitFor(object.get(), "done"), "failed process start releases both collectors");
        check(object->property("code") == -1, "failed start reports a nonzero exit");
    }
    {
        auto object = create(engine, R"(
import QtQml
import Quickshell.Io 1.0
Process {
    command: ["/bin/cat"]
    stdinEnabled: true
    running: true
    property bool done: false
    property string received: ""
    stdout: SplitParser { onRead: data => { received = data; running = false; } }
    onStarted: write("stdin works\n")
    onExited: done = true
})");
        check(waitFor(object.get(), "done"), "process stdin and terminate");
        check(object->property("received") == "stdin works", "stdin forwarded to stdout parser");
    }
    {
        QLocalServer server;
        const auto socketPath = dir.filePath("ipc.sock");
        check(server.listen(socketPath), "temporary unix socket listener");
        engine.rootContext()->setContextProperty("socketPath", socketPath);
        QByteArray received;
        QObject::connect(&server, &QLocalServer::newConnection, &server, [&] {
            auto *peer = server.nextPendingConnection();
            QObject::connect(peer, &QLocalSocket::readyRead, peer, [&, peer] {
                received.append(peer->readAll());
                if (!received.contains('\n')) return;
                peer->write("first\nsecond\npartial");
                peer->flush();
                peer->disconnectFromServer();
            });
        });
        auto object = create(engine, R"(
import QtQml
import Quickshell.Io 1.0
Socket {
    path: socketPath
    connected: true
    property bool done: false
    property string received: ""
    property int connections: 0
    parser: SplitParser { onRead: data => received += data + "|" }
    onConnectionStateChanged: {
        if (connected) { connections++; write("request\n"); flush(); return; }
        done = true;
    }
})");
        check(waitFor(object.get(), "done"), "unix socket exchange and disconnect");
        check(received == "request\n", "socket request reaches real server");
        check(object->property("received") == "first|second|partial|", "split parser handles multiple frames and EOF tail");
        check(object->property("connections") == 1, "socket signals established state");
    }
    {
        QLocalServer server;
        const auto socketPath = dir.filePath("reentrant.sock");
        check(server.listen(socketPath), "reentrant socket listener");
        engine.rootContext()->setContextProperty("socketPath", socketPath);
        QObject::connect(&server, &QLocalServer::newConnection, &server, [&] {
            server.nextPendingConnection()->write("first\nsecond\nthird\n");
        });
        auto object = create(engine, R"(
import QtQml
import Quickshell.Io 1.0
Socket {
    id: root
    path: socketPath
    connected: true
    property bool done: false
    property string received: ""
    parser: SplitParser {
        onRead: data => {
            received += data + "|";
            if (data === "first") root.connected = false;
        }
    }
    onConnectionStateChanged: { if (!connected) done = true; }
})");
        check(waitFor(object.get(), "done"), "parser may disconnect during a read callback");
        check(object->property("received") == "first|second|third|", "disconnect must preserve separate queued frames");
    }
    {
        engine.rootContext()->setContextProperty("socketPath", dir.filePath("absent.sock"));
        auto object = create(engine, R"(
import QtQml
import Quickshell.Io 1.0
Socket {
    path: socketPath
    connected: true
    property bool done: false
    onError: error => done = true
})");
        check(waitFor(object.get(), "done"), "failed socket dial reports error");
        check(!object->property("connected").toBool(), "failed socket dial never claims connection");
    }
    {
        auto object = create(engine, R"(
import QtQml
import Quickshell.Io 1.0
FileView {
    path: testPath
    function savePending() { setText("pending save survives destruction"); }
})");
        check(QMetaObject::invokeMethod(object.get(), "savePending"), "asynchronous write requested");
        object.reset();
        check(contents(path) == "pending save survives destruction", "destruction flushes queued persistence");
    }
    {
        const auto bytesPath = dir.filePath("image bytes");
        put(bytesPath, QByteArray("GIF89a\x00\xff", 8));
        engine.rootContext()->setContextProperty("bytesPath", bytesPath);
        auto object = create(engine, R"(
import QtQml
import Quickshell.Io 1.0
FileView {
    path: bytesPath
    blockLoading: true
    property bool done: false
    property bool binaryOk: false
    onLoaded: {
        const bytes = new Uint8Array(data());
        binaryOk = bytes.length === 8 && bytes[0] === 71 && bytes[6] === 0 && bytes[7] === 255;
        done = true;
    }
})");
        check(waitFor(object.get(), "done"), "binary file loaded");
        check(object->property("binaryOk").toBool(), "FileView data converts to a JS ArrayBuffer for image sniffing");
    }
    {
        const auto laterPath = dir.filePath("future/directory/theme.json");
        engine.rootContext()->setContextProperty("laterPath", laterPath);
        auto object = create(engine, R"(
import QtQml
import Quickshell.Io 1.0
FileView {
    path: laterPath
    watchChanges: true
    printErrors: false
    property bool done: false
    onFileChanged: reload()
    onLoaded: done = text() === "new theme"
})");
        QDir().mkpath(QFileInfo(laterPath).absolutePath());
        put(laterPath, "new theme");
        check(waitFor(object.get(), "done"), "watching a missing directory discovers newly published theme");
    }
    {
        put(path, "{}");
        auto object = create(engine, R"(
import QtQml
import Quickshell.Io 1.0
FileView {
    path: testPath
    property bool loaded: false
    onLoaded: loaded = true
    JsonAdapter { property string themeMode: "auto" }
})");
        check(waitFor(object.get(), "loaded"), "CacheData can shadow FileView loaded with its own property");
    }
    {
        put(path, "first");
        const auto otherPath = dir.filePath("other.json");
        put(otherPath, "latest");
        engine.rootContext()->setContextProperty("otherPath", otherPath);
        auto object = create(engine, R"(
import QtQml
import Quickshell.Io 1.0
FileView {
    path: testPath
    property bool done: false
    property string result: ""
    property int loads: 0
    onLoaded: { result = text(); loads++; done = true; }
    Component.onCompleted: path = otherPath
})");
        check(waitFor(object.get(), "done"), "path update loads its latest generation");
        check(object->property("result") == "latest", "stale async file result is discarded");
        check(object->property("loads") == 1, "initial deferred preload does not queue a duplicate read");
    }
    {
        auto object = create(engine, R"(
import QtQml
import Quickshell.Io 1.0
Process {
    command: ["/bin/sh", "-c", "printf 'again'"]
    running: true
    property int runs: 0
    property bool done: false
    property string collected: ""
    stdout: StdioCollector { onStreamFinished: collected += text + "|" }
    onExited: { runs++; if (runs < 2) { running = true; return; } done = true; }
})");
        check(waitFor(object.get(), "done"), "process can restart inside exited handler");
        check(object->property("collected") == "again|again|", "collectors reset between process runs");
    }
    {
        auto object = create(engine, R"(
import QtQml
import Quickshell.Io 1.0
Process {
    command: ["/usr/bin/head", "-c", "17000000", "/dev/zero"]
    running: true
    property bool done: false
    property int code: 0
    property int collected: 0
    stdout: StdioCollector { onStreamFinished: collected = data.byteLength }
    onExited: exitCode => { code = exitCode; done = true; }
})");
        check(waitFor(object.get(), "done"), "unbounded child output is terminated");
        check(object->property("code") == -1, "collector overflow never reports success");
        check(object->property("collected").toInt() <= 16 * 1024 * 1024, "collector memory is capped");
    }
    {
        auto object = create(engine, R"(
import QtQml
import Quickshell.Io 1.0
FileView {
    path: testPath
    function queueWrites() { for (let i = 0; i < 200; i++) setText("write " + i); }
})");
        check(QMetaObject::invokeMethod(object.get(), "queueWrites"), "queue repeated persistence updates");
        object.reset();
        check(contents(path) == "write 199", "bounded write coalescing retains latest persisted state");
    }
    {
        auto object = create(engine, R"(
import QtQml
import Quickshell.Io 1.0
Process {
    command: ["/bin/sleep", "30"]
    running: true
    property bool startedOk: false
    onStarted: startedOk = true
})");
        check(waitFor(object.get(), "startedOk"), "long-running subprocess started");
        const QPointer<QProcess> child(object->findChild<QProcess *>());
        check(bool(child), "running process is native QProcess");
        QEventLoop reaped;
        QTimer deadline;
        deadline.setSingleShot(true);
        QObject::connect(child, &QObject::destroyed, &reaped, &QEventLoop::quit);
        QObject::connect(&deadline, &QTimer::timeout, &reaped, &QEventLoop::quit);
        deadline.start(5000);
        object.reset();
        if (child) reaped.exec();
        check(!child, "destroyed owner terminates and asynchronously reaps child");
    }
    {
        auto object = create(engine, R"(
import QtQml
import Quickshell.Io 1.0
FileView {
    path: testDirectory
    blockWrites: true
    printErrors: false
    property int code: -1
    property bool falselySaved: false
    onSaveFailed: error => code = error
    onSaved: falselySaved = true
    Component.onCompleted: setText("must fail")
})");
        check(object->property("code") == 4, "invalid atomic target reports save failure");
        check(!object->property("falselySaved").toBool(), "failed write does not emit saved");
    }
    {
        engine.rootContext()->setContextProperty("laterPath", dir.filePath("delayed-font.json"));
        auto object = create(engine, R"(
import QtQml
import Quickshell.Io 1.0
FileView {
    path: laterPath
    watchChanges: true
    printErrors: false
    property bool failed: false
    property bool done: false
    onLoadFailed: failed = true
    onFileChanged: reload()
    onLoaded: done = text() === "font appeared"
})");
        check(waitFor(object.get(), "failed"), "initial missing font reports failure");
        put(dir.filePath("delayed-font.json"), "font appeared");
        check(waitFor(object.get(), "done"), "watching missing font discovers later creation");
    }
    {
        auto object = create(engine, R"(
import QtQml
import Quickshell.Io 1.0
FileView {
    path: testPath
    blockLoading: true
    blockWrites: true
    watchChanges: true
    property bool done: false
    onFileChanged: done = true
    Component.onCompleted: { text(); setText("own write watched"); }
})");
        check(waitFor(object.get(), "done"), "successful own writes notify file watcher");
    }
    {
        auto object = create(engine, R"(
import QtQml
import Quickshell.Io 1.0
Process {
    command: ["/bin/sh", "-c", "printf 'one::two::tail'"]
    running: true
    property bool done: false
    property string received: ""
    stdout: SplitParser { splitMarker: "::"; onRead: data => received += data + "|" }
    onExited: done = true
})");
        check(waitFor(object.get(), "done"), "custom split marker process completes");
        check(object->property("received") == "one|two|tail|", "custom split marker and EOF preserved");
    }
    {
        const auto emptyPath = dir.filePath("empty.json");
        engine.rootContext()->setContextProperty("emptyPath", emptyPath);
        auto object = create(engine, R"(
import QtQml
import Quickshell.Io 1.0
FileView {
    path: emptyPath
    blockLoading: true
    blockWrites: true
    printErrors: false
    property bool done: false
    onSaved: done = true
    Component.onCompleted: { text(); setText(""); }
})");
        check(object->property("done").toBool(), "empty content write succeeds after missing-file read");
        check(QFileInfo::exists(emptyPath) && contents(emptyPath).isEmpty(), "empty content creates a real file");
    }
    {
        QLocalServer server;
        const auto socketPath = dir.filePath("fragmented.sock");
        check(server.listen(socketPath), "fragmented socket listener");
        engine.rootContext()->setContextProperty("socketPath", socketPath);
        QObject::connect(&server, &QLocalServer::newConnection, &server, [&] {
            auto *peer = server.nextPendingConnection();
            const QByteArray prefix = QByteArray::fromHex("636166c3");
            peer->write(prefix);
            QObject::connect(peer, &QLocalSocket::readyRead, peer, [peer] {
                if (peer->readAll() != "continue\n") return;
                peer->write(QByteArray::fromHex("a93a3a7461696c"));
                peer->disconnectFromServer();
            });
        });
        auto object = create(engine, R"(
import QtQml
import Quickshell.Io 1.0
Socket {
    path: socketPath
    connected: true
    property bool done: false
    property string received: ""
    parser: SplitParser { splitMarker: "::"; onRead: data => received += data + "|" }
    function continueWrite() { write("continue
"); flush(); }
    onConnectionStateChanged: { if (!connected) done = true; }
})");
        QTimer::singleShot(10, object.get(), [&] {
            check(QMetaObject::invokeMethod(object.get(), "continueWrite"), "fragment continuation request");
        });
        check(waitFor(object.get(), "done"), "fragmented socket stream completes");
        check(object->property("received") == QString::fromUtf8("café|tail|"), "split UTF-8 bytes and split markers preserve characters");
    }
    {
        put(path, R"({"mode":"stored"})");
        auto object = create(engine, R"(
import QtQml
import Quickshell.Io 1.0
FileView {
    id: root
    path: testPath
    blockLoading: true
    blockWrites: true
    property alias mode: original.mode
    JsonAdapter { id: original; property string mode: "default" }
    property Component replacement: Component { JsonAdapter { property string mode: "fresh" } }
    function swapAdapter() { adapter = replacement.createObject(root); adapter.mode = "swapped"; writeAdapter(); }
})");
        check(object->property("mode") == "stored", "initial JSON adapter applies stored state");
        check(QMetaObject::invokeMethod(object.get(), "swapAdapter"), "replace live JSON adapter");
        check(QJsonDocument::fromJson(contents(path)).object()["mode"] == "swapped", "replacement adapter persists its dynamic properties");
    }
    qInfo("io_test: all checks passed");
}
