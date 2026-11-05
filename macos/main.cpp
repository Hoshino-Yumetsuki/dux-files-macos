#include "runtime/io.h"
#include "runtime/shell.h"

#include <QApplication>
#include <QCommandLineParser>
#include <QDir>
#include <QFileInfo>
#include <QFileOpenEvent>
#include <QFileSystemWatcher>
#include <QJsonArray>
#include <QJsonDocument>
#include <QJsonObject>
#include <QLocalSocket>
#include <QMessageBox>
#include <QMenuBar>
#include <QMenu>
#include <QPointer>
#include <QProcess>
#include <QQmlApplicationEngine>
#include <QQuickStyle>
#include <QQuickWindow>
#include <memory>
#include <QStandardPaths>
#include <QTimer>
#include <QUrl>

static QString normalizePath(const QString &path) {
    const QUrl url(path);
    if (!url.scheme().isEmpty() && !url.isLocalFile())
        return path;
    return QFileInfo(url.isLocalFile() ? url.toLocalFile() : path).absoluteFilePath();
}

class Application : public QApplication {
public:
    Application(int &argc, char **argv) : QApplication(argc, argv) {
        openDebounce.setSingleShot(true);
        openDebounce.setInterval(50);
        connect(&openDebounce, &QTimer::timeout, this, &Application::openFiles);
    }

    QStringList pendingFiles;
    QString socketPath;
    bool ready = false;
    bool quitApproved = false;
    QObject *shellRoot = nullptr;

    void scheduleOpenFiles() {
        openDebounce.start(50);
    }

    void openFiles() {
        if (!ready || pendingFiles.isEmpty() || openSocket)
            return;
        const QStringList files = pendingFiles;
        auto *socket = new QLocalSocket(this);
        openSocket = socket;
        auto *timeout = new QTimer(socket);
        timeout->setSingleShot(true);
        auto finish = [this, socket, timeout, files](const QString &error, bool retry = false) {
            if (openSocket != socket)
                return;
            timeout->stop();
            socket->disconnect(this);
            socket->abort();
            socket->deleteLater();
            openSocket = nullptr;
            openResponse.clear();
            if (retry && ++openAttempts < 3) {
                openDebounce.start(250);
                return;
            }
            if (!error.isEmpty())
                qWarning().noquote() << "Could not deliver open paths:" << error;
            pendingFiles.remove(0, qMin(files.size(), pendingFiles.size()));
            openAttempts = 0;
            scheduleOpenFiles();
        };
        const QJsonObject request{{"id", 1}, {"method", "ui.open"},
            {"params", QJsonObject{{"paths", QJsonArray::fromStringList(files)}}}};
        connect(socket, &QLocalSocket::connected, socket, [socket, request] {
            socket->setProperty("requestSent", true);
            socket->write(QJsonDocument(request).toJson(QJsonDocument::Compact) + '\n');
            socket->flush();
        });
        connect(socket, &QLocalSocket::readyRead, this, [this, socket, finish] {
            openResponse.append(socket->readAll());
            if (openResponse.size() > 1024 * 1024) {
                finish(QStringLiteral("Open response exceeded 1 MiB"));
                return;
            }
            while (true) {
                const qsizetype end = openResponse.indexOf('\n');
                if (end < 0)
                    return;
                const QByteArray line = openResponse.left(end);
                openResponse.remove(0, end + 1);
                const QJsonDocument response = QJsonDocument::fromJson(line);
                if (!response.isObject() || response.object().value("id").toInt() != 1)
                    continue;
                finish(response.object().value("error").toString());
                return;
            }
        });
        connect(socket, &QLocalSocket::errorOccurred, this, [socket, finish] {
            finish(socket->errorString(), !socket->property("requestSent").toBool());
        });
        connect(socket, &QLocalSocket::disconnected, this, [finish] {
            finish(QStringLiteral("Backend closed before acknowledging open paths"));
        });
        connect(timeout, &QTimer::timeout, this, [finish] {
            finish(QStringLiteral("Timed out delivering open paths"));
        });
        timeout->start(5000);
        socket->connectToServer(socketPath);
    }

protected:
    bool event(QEvent *event) override {
        if (event->type() == QEvent::Quit) {
            if (!quitApproved && ready && shellRoot && shellRoot->metaObject()->indexOfMethod("requestApplicationQuit()") >= 0) {
                event->ignore();
                QMetaObject::invokeMethod(shellRoot, "requestApplicationQuit", Qt::QueuedConnection);
                return true;
            }
            return QCoreApplication::event(event);
        }
        if (event->type() != QEvent::FileOpen)
            return QApplication::event(event);
        auto *fileEvent = static_cast<QFileOpenEvent *>(event);
        pendingFiles.append(normalizePath(fileEvent->url().isLocalFile()
            ? fileEvent->url().toLocalFile()
            : fileEvent->url().toString()));
        scheduleOpenFiles();
        return true;
    }

private:
    QTimer openDebounce;
    QByteArray openResponse;
    QLocalSocket *openSocket = nullptr;
    int openAttempts = 0;
};

static void setDefaultEnvironment(const char *key, const QString &value) {
    if (!qEnvironmentVariableIsSet(key))
        qputenv(key, value.toUtf8());
}
static void installNativeMenus() {
    auto *bar = new QMenuBar;
    auto activeWindow = std::make_shared<QPointer<QWindow>>();
    QObject::connect(qApp, &QGuiApplication::focusWindowChanged, bar, [activeWindow](QWindow *window) {
        if (window && window->metaObject()->indexOfMethod("nativeMenuAction(QVariant)") >= 0) *activeWindow = window;
    });
    auto add = [bar, activeWindow](QMenu *menu, const QString &title, const char *id, const QKeySequence &shortcut = {}) {
        auto *action = menu->addAction(title);
        if (!shortcut.isEmpty()) action->setShortcut(shortcut);
        const QString name = QString::fromLatin1(id);
        QObject::connect(menu, &QMenu::aboutToShow, action, [activeWindow, action, name] {
            QVariant enabled;
            if (*activeWindow) QMetaObject::invokeMethod(*activeWindow, "nativeMenuEnabled", Q_RETURN_ARG(QVariant, enabled), Q_ARG(QVariant, name));
            action->setEnabled(enabled.toBool());
        });
        QObject::connect(action, &QAction::triggered, bar, [activeWindow, name] {
            if (*activeWindow) QMetaObject::invokeMethod(*activeWindow, "nativeMenuAction", Q_ARG(QVariant, name));
        });
    };
    auto *file = bar->addMenu(QObject::tr("File"));
    add(file, QObject::tr("New Window"), "newWindow", QKeySequence::New);
    add(file, QObject::tr("New Tab"), "newTab", QKeySequence(Qt::CTRL | Qt::Key_T));
    add(file, QObject::tr("New Folder"), "newFolder", QKeySequence(Qt::CTRL | Qt::SHIFT | Qt::Key_N));
    file->addSeparator();
    add(file, QObject::tr("Open"), "open", QKeySequence::Open);
    add(file, QObject::tr("Quick Look"), "quickLook");
    add(file, QObject::tr("Share…"), "shareFiles");
    add(file, QObject::tr("Get Info"), "properties", QKeySequence(Qt::CTRL | Qt::Key_I));
    add(file, QObject::tr("Finder Tags…"), "finderTags");
    add(file, QObject::tr("Rename"), "rename");
    add(file, QObject::tr("Move to Trash"), "trash");
    auto *edit = bar->addMenu(QObject::tr("Edit"));
    add(edit, QObject::tr("Cut"), "cut", QKeySequence::Cut);
    add(edit, QObject::tr("Copy"), "copy", QKeySequence::Copy);
    add(edit, QObject::tr("Paste"), "paste", QKeySequence::Paste);
    add(edit, QObject::tr("Select All"), "selectAll", QKeySequence::SelectAll);
    auto *view = bar->addMenu(QObject::tr("View"));
    add(view, QObject::tr("List"), "listView");
    add(view, QObject::tr("Grid"), "gridView");
    add(view, QObject::tr("Columns"), "columnsView");
    add(view, QObject::tr("Show or Hide Sidebar"), "toggleSidebar");
    add(view, QObject::tr("Show or Hide Hidden Files"), "showHidden");
    add(view, QObject::tr("Reload"), "reload", QKeySequence::Refresh);
    auto *go = bar->addMenu(QObject::tr("Go"));
    add(go, QObject::tr("Back"), "back", QKeySequence::Back);
    add(go, QObject::tr("Forward"), "forward", QKeySequence::Forward);
    add(go, QObject::tr("Home"), "home");
    add(go, QObject::tr("Go to Folder…"), "editPath");
    add(go, QObject::tr("Search"), "search", QKeySequence::Find);
    auto *window = bar->addMenu(QObject::tr("Window"));
    add(window, QObject::tr("Close Tab"), "closeTab", QKeySequence::Close);
    add(window, QObject::tr("Next Tab"), "nextTab");
    add(window, QObject::tr("Previous Tab"), "previousTab");
    auto *appMenu = bar->addMenu(QObject::tr("Dux Files"));
    auto *settings = appMenu->addAction(QObject::tr("Settings…"));
    settings->setMenuRole(QAction::PreferencesRole);
    QObject::connect(settings, &QAction::triggered, bar, [activeWindow] { if (*activeWindow) QMetaObject::invokeMethod(*activeWindow, "nativeMenuAction", Q_ARG(QVariant, QStringLiteral("settings"))); });
    auto *quit = appMenu->addAction(QObject::tr("Quit Dux Files"));
    quit->setMenuRole(QAction::QuitRole);
    QObject::connect(quit, &QAction::triggered, qApp, &QCoreApplication::quit);
}

int main(int argc, char **argv) {
    setDefaultEnvironment("QT_QUICK_CONTROLS_STYLE", "Basic");
    Application app(argc, argv);
    app.setApplicationName("Dux Files");
    app.setOrganizationName("Dux Labs");
    app.setOrganizationDomain("duxlabs.dev");
    app.setQuitOnLastWindowClosed(false);
    QQuickStyle::setStyle("Basic");
    installNativeMenus();
    DuxCocoa::registerServices();

    const QString home = QDir::homePath();
    setDefaultEnvironment("XDG_CONFIG_HOME", home + "/Library/Application Support");
    setDefaultEnvironment("XDG_DATA_HOME", home + "/Library/Application Support/dfiles/data");
    setDefaultEnvironment("XDG_STATE_HOME", home + "/Library/Application Support/state");
    setDefaultEnvironment("XDG_CACHE_HOME", home + "/Library/Caches");
    QStringList searchPath = qEnvironmentVariable("PATH").split(':');
    searchPath.prepend(QCoreApplication::applicationDirPath());
    qputenv("PATH", searchPath.join(':').toUtf8());

    QCommandLineParser parser;
    parser.setApplicationDescription("Dux Files — original Dux QML UI on macOS");
    parser.addHelpOption();
    parser.addOption({{"p", "config"}, "QML shell directory", "directory"});
    parser.addOption({"no-backend", "Use the supplied DFILES_SOCKET without starting a daemon"});
    parser.addOption({"smoke-test", "Render a window and exit after validating startup"});
    parser.addOption({"screenshot", "Save a rendered window during the startup check", "file"});
    parser.addPositionalArgument("paths", "Folders or files to open", "[paths...]");
    parser.process(app);
    app.pendingFiles = parser.positionalArguments();
    for (QString &path : app.pendingFiles)
        path = normalizePath(path);

    QString qmlRoot = QDir::cleanPath(QCoreApplication::applicationDirPath() + "/../Resources/qml");
    if (!QFileInfo::exists(qmlRoot + "/qs/shell.qml"))
        qmlRoot = QStringLiteral(DFILES_QML_ROOT);
    QString shell = parser.value("config");
    if (shell.isEmpty())
        shell = qmlRoot + "/qs";
    shell = QFileInfo(shell).absoluteFilePath();
    if (parser.isSet("config"))
        qmlRoot = QFileInfo(shell).absolutePath();

    QQmlApplicationEngine engine;
    engine.addImportPath(qmlRoot);
    registerIoTypes();
    registerShellTypes(&engine, shell);
    QObject::connect(&engine, &QQmlEngine::quit, &app, [&app] {
        app.quitApproved = true;
        app.quit();
    });
    QObject::connect(&engine, &QQmlEngine::exit, &app, &QCoreApplication::exit);
    QObject::connect(&engine, &QQmlApplicationEngine::objectCreationFailed, &app, [] {
        QCoreApplication::exit(1);
    }, Qt::QueuedConnection);

    bool loaded = false;
    bool failed = false;
    QProcess backend;
    QFileSystemWatcher watcher;
    QTimer deadline;
    deadline.setSingleShot(true);
    auto fail = [&](const QString &message) {
        if (failed)
            return;
        failed = true;
        qCritical().noquote() << message;
        if (!parser.isSet("smoke-test") && qEnvironmentVariable("QT_QPA_PLATFORM") != "offscreen")
            QMessageBox::critical(nullptr, app.applicationName(), message);
        app.exit(1);
    };
    auto load = [&] {
        if (loaded || failed)
            return;
        if (!parser.isSet("no-backend") && !QFileInfo::exists(app.socketPath))
            return;
        loaded = true;
        deadline.stop();
        qputenv("DFILES_SOCKET", app.socketPath.toUtf8());
        engine.load(QUrl::fromLocalFile(shell + "/shell.qml"));
        app.ready = !engine.rootObjects().isEmpty();
        app.shellRoot = app.ready ? engine.rootObjects().first() : nullptr;
        app.scheduleOpenFiles();
        if (!parser.isSet("smoke-test") && !parser.isSet("screenshot"))
            return;
        QTimer::singleShot(1800, &app, [&] {
            for (QWindow *window : app.topLevelWindows()) {
                auto *quick = qobject_cast<QQuickWindow *>(window);
                if (!quick || !quick->isVisible())
                    continue;
                const QImage image = quick->grabWindow();
                if (image.isNull()) {
                    fail("Dux Files did not render a frame");
                    return;
                }
                if (parser.isSet("screenshot") && !image.save(parser.value("screenshot"))) {
                    fail("Could not save the window screenshot");
                    return;
                }
                qInfo() << "Dux Files rendered" << image.size();
                app.exit(0);
                return;
            }
            fail("Dux Files did not create a visible window");
        });
    };
    QObject::connect(&app, &QGuiApplication::applicationStateChanged, &app, [&](Qt::ApplicationState state) {
        if (state != Qt::ApplicationActive || !app.ready)
            return;
        for (QWindow *window : app.topLevelWindows()) {
            if (qobject_cast<QQuickWindow *>(window) && window->isVisible())
                return;
        }
        for (QObject *root : engine.rootObjects())
            QMetaObject::invokeMethod(root, "showAndFocus");
    });
    QObject::connect(&deadline, &QTimer::timeout, &app, [&] { fail("Timed out starting the Dux Files backend"); });
    QObject::connect(&watcher, &QFileSystemWatcher::directoryChanged, &app, load);
    QObject::connect(&backend, &QProcess::errorOccurred, &app, [&](QProcess::ProcessError) { fail(backend.errorString()); });
    QObject::connect(&backend, &QProcess::finished, &app, [&](int code, QProcess::ExitStatus status) {
        if (failed)
            return;
        if (status == QProcess::NormalExit && code == 0) {
            app.quitApproved = true;
            app.quit();
            return;
        }
        fail(QString("Dux Files backend stopped (%1)").arg(code));
    });

    if (parser.isSet("no-backend")) {
        app.socketPath = qEnvironmentVariable("DFILES_SOCKET");
        QTimer::singleShot(0, &app, load);
    } else {
        QString runtime = qEnvironmentVariable("XDG_RUNTIME_DIR");
        if (runtime.isEmpty())
            runtime = QDir::tempPath();
        runtime = QFileInfo(runtime).absoluteFilePath();
        qputenv("XDG_RUNTIME_DIR", runtime.toUtf8());
        if (!QDir().mkpath(runtime) || !watcher.addPath(runtime)) {
            qCritical().noquote() << "Could not watch the Dux Files runtime directory:" << runtime;
            return 1;
        }
        const QString binary = QCoreApplication::applicationDirPath() + "/dfiles";
        qputenv("DFILES_UI_PID", QByteArray::number(QCoreApplication::applicationPid()));
        backend.setProgram(binary);
        backend.setArguments({"daemon"});
        backend.setProcessChannelMode(QProcess::ForwardedChannels);
        QObject::connect(&backend, &QProcess::started, &app, [&, runtime] {
            app.socketPath = QDir(runtime).filePath(QString("dfiles-%1.sock").arg(backend.processId()));
            load();
        });
        deadline.start(15000);
        backend.start();
    }
    const int result = app.exec();
    backend.disconnect(&app);
    if (backend.state() != QProcess::NotRunning) {
        backend.terminate();
        if (!backend.waitForFinished(5000)) {
            backend.kill();
            backend.waitForFinished(1000);
        }
    }
    return result;
}
