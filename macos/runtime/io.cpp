#include "io.h"

#include <QDir>
#include <QFile>
#include <QFileInfo>
#include <QFileSystemWatcher>
#include <QFutureWatcher>
#include <QJsonDocument>
#include <QJsonObject>
#include <QJSValue>
#include <QLocalSocket>
#include <QMetaProperty>
#include <QPointer>
#include <QProcess>
#include <QPromise>
#include <QQmlEngine>
#include <QQmlInfo>
#include <QQmlParserStatus>
#include <QRunnable>
#include <QSaveFile>
#include <QThreadPool>
#include <QUrl>
#include <memory>
#include <optional>
#include <utility>

#undef stdout
#undef stderr

namespace {
constexpr qsizetype fileLimit = 64 * 1024 * 1024;
constexpr qsizetype streamLimit = 16 * 1024 * 1024;
constexpr qsizetype chunkSize = 64 * 1024;
constexpr qsizetype writeBufferLimit = 1024 * 1024;

QString localPath(const QString &path) {
    const QUrl url(path);
    if (url.isLocalFile()) return url.toLocalFile();
    if (url.scheme() == QLatin1String("qrc")) return ':' + url.path();
    return path;
}

QVariant plainVariant(const QVariant &value) {
    if (value.metaType() == QMetaType::fromType<QJSValue>())
        return value.value<QJSValue>().toVariant();
    return value;
}
}

class FileViewError : public QObject {
    Q_OBJECT
public:
    enum Enum { Success = 0, Unknown = 1, FileNotFound = 2, PermissionDenied = 3, NotAFile = 4 };
    Q_ENUM(Enum)
    using QObject::QObject;
};

class JsonAdapter : public QObject, public QQmlParserStatus {
    Q_OBJECT
    Q_INTERFACES(QQmlParserStatus)
public:
    using QObject::QObject;
    void classBegin() override {}
    void componentComplete() override {
        m_complete = true;
        const auto slot = staticMetaObject.method(staticMetaObject.indexOfSlot("propertyUpdated()"));
        for (int i = staticMetaObject.propertyCount(); i < metaObject()->propertyCount(); ++i) {
            const auto property = metaObject()->property(i);
            if (property.isReadable() && property.hasNotifySignal())
                connect(this, property.notifySignal(), this, slot, Qt::UniqueConnection);
        }
        if (!m_pending.isEmpty()) {
            const auto bytes = std::exchange(m_pending, {});
            deserialize(bytes);
        }
    }
    void deserialize(const QByteArray &bytes) {
        if (!m_complete) {
            m_pending = bytes;
            return;
        }
        if (bytes.isEmpty()) return;
        QJsonParseError error;
        const auto document = QJsonDocument::fromJson(bytes, &error);
        if (error.error != QJsonParseError::NoError || !document.isObject()) {
            qmlWarning(this) << "Cannot read JSON adapter: " << error.errorString();
            return;
        }
        const bool wasApplying = std::exchange(m_applying, true);
        const QPointer<JsonAdapter> guard(this);
        const auto object = document.object();
        for (int i = staticMetaObject.propertyCount(); i < metaObject()->propertyCount(); ++i) {
            const auto property = metaObject()->property(i);
            const auto value = object.find(QString::fromUtf8(property.name()));
            if (value == object.end() || !property.isWritable()) continue;
            auto variant = value->toVariant();
            if (plainVariant(property.read(this)) == variant) continue;
            if (property.metaType() == QMetaType::fromType<QVariant>()) {
                auto *engine = qmlEngine(this);
                if (engine) variant = QVariant::fromValue(engine->toScriptValue(variant));
            }
            const bool written = property.write(this, variant);
            if (!guard) return;
            if (!written) qmlWarning(this) << "Cannot set JSON adapter property " << property.name();
        }
        m_applying = wasApplying;
    }
    QByteArray serialize() const {
        QJsonObject object;
        for (int i = staticMetaObject.propertyCount(); i < metaObject()->propertyCount(); ++i) {
            const auto property = metaObject()->property(i);
            if (!property.isReadable() || !property.hasNotifySignal()) continue;
            object.insert(QString::fromUtf8(property.name()),
                          QJsonValue::fromVariant(plainVariant(property.read(this))));
        }
        return QJsonDocument(object).toJson(QJsonDocument::Indented);
    }
signals:
    void adapterUpdated();
private slots:
    void propertyUpdated() {
        if (!m_applying) emit adapterUpdated();
    }
private:
    bool m_complete = false;
    bool m_applying = false;
    QByteArray m_pending;
};

namespace {
struct FileJob {
    QString path;
    QByteArray bytes;
    quint64 generation = 0;
    bool write = false;
    bool atomic = true;
};
struct FileResult {
    QByteArray bytes;
    FileViewError::Enum error = FileViewError::Success;
    QString message;
};

FileViewError::Enum fileError(const QFileDevice &file) {
    if (file.error() == QFileDevice::PermissionsError) return FileViewError::PermissionDenied;
    return FileViewError::Unknown;
}

FileResult performFileJob(const FileJob &job) {
    const QFileInfo info(job.path);
    if (info.exists() && !info.isFile())
        return {{}, FileViewError::NotAFile, QStringLiteral("Path is not a regular file")};
    if (!job.write) {
        QFile file(job.path);
        if (!file.open(QIODevice::ReadOnly)) {
            const auto error = info.exists() ? fileError(file) : FileViewError::FileNotFound;
            return {{}, error, file.errorString()};
        }
        if (file.size() > fileLimit)
            return {{}, FileViewError::Unknown, QStringLiteral("File exceeds the 64 MiB limit")};
        auto bytes = file.read(fileLimit + 1);
        if (file.error() != QFileDevice::NoError) return {{}, fileError(file), file.errorString()};
        if (bytes.size() > fileLimit)
            return {{}, FileViewError::Unknown, QStringLiteral("File exceeds the 64 MiB limit")};
        return {bytes, FileViewError::Success, {}};
    }
    if (job.path.isEmpty())
        return {{}, FileViewError::FileNotFound, QStringLiteral("No file path was provided")};
    if (job.bytes.size() > fileLimit)
        return {{}, FileViewError::Unknown, QStringLiteral("File exceeds the 64 MiB limit")};
    if (!QDir().mkpath(info.absolutePath()))
        return {{}, FileViewError::PermissionDenied, QStringLiteral("Cannot create parent directory")};
    std::unique_ptr<QFileDevice> file;
    if (job.atomic) {
        auto safe = std::make_unique<QSaveFile>(job.path);
        safe->setDirectWriteFallback(false);
        file = std::move(safe);
    } else {
        file = std::make_unique<QFile>(job.path);
    }
    if (!file->open(QIODevice::WriteOnly | QIODevice::Truncate))
        return {{}, fileError(*file), file->errorString()};
    if (file->write(job.bytes) != job.bytes.size() || !file->flush())
        return {{}, fileError(*file), file->errorString()};
    if (job.atomic && !static_cast<QSaveFile *>(file.get())->commit())
        return {{}, fileError(*file), file->errorString()};
    if (!job.atomic) file->close();
    return {job.bytes, FileViewError::Success, {}};
}

QThreadPool &filePool() {
    static QThreadPool pool;
    static const bool initialized = [] {
        pool.setMaxThreadCount(2);
        pool.setExpiryTimeout(5000);
        return true;
    }();
    Q_UNUSED(initialized);
    return pool;
}
}

class FileView : public QObject, public QQmlParserStatus {
    Q_OBJECT
    Q_INTERFACES(QQmlParserStatus)
    Q_CLASSINFO("DefaultProperty", "adapter")
    Q_PROPERTY(QString path READ path WRITE setPath NOTIFY pathChanged)
    Q_PROPERTY(bool preload READ preload WRITE setPreload NOTIFY preloadChanged)
    Q_PROPERTY(bool blockLoading READ blockLoading WRITE setBlockLoading NOTIFY blockLoadingChanged)
    Q_PROPERTY(bool blockWrites READ blockWrites WRITE setBlockWrites NOTIFY blockWritesChanged)
    Q_PROPERTY(bool atomicWrites READ atomicWrites WRITE setAtomicWrites NOTIFY atomicWritesChanged)
    Q_PROPERTY(bool printErrors READ printErrors WRITE setPrintErrors NOTIFY printErrorsChanged)
    Q_PROPERTY(bool watchChanges READ watchChanges WRITE setWatchChanges NOTIFY watchChangesChanged)
    Q_PROPERTY(bool loaded READ isLoaded NOTIFY loadedOrAsyncChanged)
    Q_PROPERTY(JsonAdapter *adapter READ adapter WRITE setAdapter NOTIFY adapterChanged)
public:
    explicit FileView(QObject *parent = nullptr) : QObject(parent) {
        connect(&m_watcher, &QFutureWatcher<FileResult>::finished, this, &FileView::finishJob);
    }
    ~FileView() override {
        m_destroying = true;
        m_readRequested = false;
        if ((m_current && m_current->write) || m_pendingWrite) drainWrites();
    }
    void classBegin() override {}
    void componentComplete() override {
        m_complete = true;
        updateWatchPaths();
        if (m_adapter && m_blockLoading) {
            ensureRead();
            return;
        }
        QMetaObject::invokeMethod(this, [this] {
            if (m_preload && !m_prepared) requestRead();
        }, Qt::QueuedConnection);
    }
    QString path() const { return m_path; }
    void setPath(const QString &path) {
        const auto normalized = localPath(path);
        if (m_path == normalized) return;
        const QPointer<FileView> guard(this);
        if ((m_current && m_current->write) || m_pendingWrite) drainWrites();
        if (!guard) return;
        m_path = normalized;
        ++m_generation;
        m_prepared = false;
        m_readRequested = false;
        if (m_path.isEmpty()) {
            m_bytes.clear();
            m_loaded = false;
            emit loadedOrAsyncChanged();
            if (!guard) return;
            emit textChanged();
            if (!guard) return;
            emit dataChanged();
            if (!guard) return;
        }
        emit pathChanged();
        if (!guard) return;
        updateWatchPaths();
        if (m_complete && m_preload && !m_path.isEmpty()) requestRead();
    }
    bool preload() const { return m_preload; }
    void setPreload(bool value) {
        if (m_preload == value) return;
        m_preload = value;
        const QPointer<FileView> guard(this);
        emit preloadChanged();
        if (guard && value && m_complete && !m_prepared) requestRead();
    }
    bool blockLoading() const { return m_blockLoading; }
    void setBlockLoading(bool value) {
        if (m_blockLoading == value) return;
        m_blockLoading = value;
        emit blockLoadingChanged();
    }
    bool blockWrites() const { return m_blockWrites; }
    void setBlockWrites(bool value) {
        if (m_blockWrites == value) return;
        m_blockWrites = value;
        emit blockWritesChanged();
    }
    bool atomicWrites() const { return m_atomicWrites; }
    void setAtomicWrites(bool value) {
        if (m_atomicWrites == value) return;
        m_atomicWrites = value;
        emit atomicWritesChanged();
    }
    bool printErrors() const { return m_printErrors; }
    void setPrintErrors(bool value) {
        if (m_printErrors == value) return;
        m_printErrors = value;
        emit printErrorsChanged();
    }
    bool watchChanges() const { return m_watchChanges; }
    void setWatchChanges(bool value) {
        if (m_watchChanges == value) return;
        m_watchChanges = value;
        updateWatchPaths();
        emit watchChangesChanged();
    }
    bool isLoaded() const { return m_loaded; }
    JsonAdapter *adapter() const { return m_adapter; }
    void setAdapter(JsonAdapter *adapter) {
        if (m_adapter == adapter) return;
        if (m_adapter) disconnect(m_adapter, nullptr, this, nullptr);
        m_adapter = adapter;
        const QPointer<FileView> guard(this);
        if (adapter) {
            if (!adapter->parent()) adapter->setParent(this);
            connect(adapter, &JsonAdapter::adapterUpdated, this, &FileView::adapterUpdated);
            connect(adapter, &QObject::destroyed, this, [this] { emit adapterChanged(); });
            if (m_loaded) adapter->deserialize(m_bytes);
        }
        if (guard) emit adapterChanged();
    }
    Q_INVOKABLE QString text() {
        const QPointer<FileView> guard(this);
        ensureRead();
        return guard ? QString::fromUtf8(m_bytes) : QString();
    }
    Q_INVOKABLE QByteArray data() {
        const QPointer<FileView> guard(this);
        ensureRead();
        return guard ? m_bytes : QByteArray();
    }
    Q_INVOKABLE void reload() {
        if (m_path.isEmpty()) return;
        ++m_generation;
        m_prepared = false;
        requestRead();
    }
    Q_INVOKABLE bool waitForJob() {
        if (!m_current) return false;
        m_watcher.waitForFinished();
        finishJob();
        return true;
    }
    Q_INVOKABLE void setText(const QString &text) { setData(text.toUtf8()); }
    Q_INVOKABLE void setData(const QByteArray &bytes) {
        ++m_generation;
        m_readRequested = false;
        m_pendingWrite = FileJob{m_path, bytes, m_generation, true, m_atomicWrites};
        startJob();
        if (m_blockWrites) drainWrites();
    }
    Q_INVOKABLE void writeAdapter() {
        if (m_adapter) setData(m_adapter->serialize());
    }
signals:
    void pathChanged();
    void preloadChanged();
    void blockLoadingChanged();
    void blockWritesChanged();
    void atomicWritesChanged();
    void printErrorsChanged();
    void watchChangesChanged();
    void loadedOrAsyncChanged();
    void adapterChanged();
    void textChanged();
    void dataChanged();
    void adapterUpdated();
    void loaded();
    void loadFailed(FileViewError::Enum error);
    void saved();
    void saveFailed(FileViewError::Enum error);
    void fileChanged();
private:
    void ensureRead() {
        if (m_prepared || m_path.isEmpty()) return;
        if (!m_current && !m_pendingWrite) requestRead();
        if (!m_blockLoading || m_loaded) return;
        const QPointer<FileView> guard(this);
        while (guard && (m_current || m_readRequested || m_pendingWrite)) {
            startJob();
            if (!m_current) break;
            waitForJob();
        }
    }
    void requestRead() {
        if (m_path.isEmpty() || m_destroying) return;
        if (m_current && !m_current->write && m_current->generation == m_generation) return;
        m_readRequested = true;
        startJob();
    }
    void startJob() {
        if (m_current) return;
        if (m_pendingWrite) {
            m_current = std::exchange(m_pendingWrite, std::nullopt);
        } else {
            if (!m_readRequested || m_destroying || m_path.isEmpty()) return;
            m_readRequested = false;
            m_current = FileJob{m_path, {}, m_generation, false, true};
        }
        const auto job = *m_current;
        auto promise = std::make_shared<QPromise<FileResult>>();
        promise->start();
        const auto future = promise->future();
        filePool().start(QRunnable::create([job, promise] {
            promise->addResult(performFileJob(job));
            promise->finish();
        }));
        m_watcher.setFuture(future);
    }
    void finishJob() {
        if (!m_current || !m_watcher.isFinished()) return;
        const auto job = *std::exchange(m_current, std::nullopt);
        const auto result = m_watcher.result();
        if (m_destroying && job.write && result.error != FileViewError::Success)
            qWarning() << "Pending FileView save failed:" << job.path << result.message;
        const QPointer<FileView> guard(this);
        if (job.path == m_path && job.generation == m_generation && !m_destroying) {
            m_prepared = true;
            if (result.error == FileViewError::Success) {
                m_bytes = result.bytes;
                const bool wasLoaded = std::exchange(m_loaded, true);
                if (m_adapter && !job.write) m_adapter->deserialize(m_bytes);
                if (!guard) return;
                if (!wasLoaded) emit loadedOrAsyncChanged();
                if (!guard) return;
                emit textChanged();
                if (!guard) return;
                emit dataChanged();
                if (!guard) return;
                if (job.write) emit saved();
                else emit loaded();
            } else {
                if (m_printErrors) qmlWarning(this) << job.path << ": " << result.message;
                if (job.write) emit saveFailed(result.error);
                else emit loadFailed(result.error);
            }
        } else if (job.write && !m_destroying) {
            if (result.error == FileViewError::Success) emit saved();
            else emit saveFailed(result.error);
        }
        if (!guard) return;
        startJob();
    }
    void drainWrites() {
        const QPointer<FileView> guard(this);
        while (guard && (m_current || m_pendingWrite)) {
            if (!m_current) startJob();
            if (!m_current) break;
            m_watcher.waitForFinished();
            finishJob();
        }
    }
    void updateWatchPaths() {
        if (!m_watchChanges || m_path.isEmpty() || m_path.startsWith(':')) {
            if (m_fileWatcher) {
                disconnect(m_fileWatcher, nullptr, this, nullptr);
                m_fileWatcher->deleteLater();
                m_fileWatcher = nullptr;
            }
            return;
        }
        if (!m_fileWatcher) {
            m_fileWatcher = new QFileSystemWatcher(this);
            connect(m_fileWatcher, &QFileSystemWatcher::fileChanged, this, [this] {
                updateWatchPaths();
                notifyFileChanged();
            });
            connect(m_fileWatcher, &QFileSystemWatcher::directoryChanged, this, [this] {
                const QFileInfo info(m_path);
                const bool changed = info.exists() != m_observedExists ||
                    (info.exists() && !m_fileWatcher->files().contains(info.absoluteFilePath()));
                updateWatchPaths();
                if (changed) notifyFileChanged();
            });
        }
        const QFileInfo info(m_path);
        m_observedExists = info.exists();
        QString directory = info.absolutePath();
        while (!QFileInfo::exists(directory)) {
            const auto parent = QFileInfo(directory).absolutePath();
            if (parent == directory) break;
            directory = parent;
        }
        const QStringList files = info.exists() ? QStringList{info.absoluteFilePath()} : QStringList{};
        const QStringList dirs{directory};
        for (const auto &old : m_fileWatcher->files())
            if (!files.contains(old)) m_fileWatcher->removePath(old);
        for (const auto &old : m_fileWatcher->directories())
            if (!dirs.contains(old)) m_fileWatcher->removePath(old);
        for (const auto &file : files)
            if (!m_fileWatcher->files().contains(file)) m_fileWatcher->addPath(file);
        if (!m_fileWatcher->directories().contains(directory)) m_fileWatcher->addPath(directory);
    }
    void notifyFileChanged() {
        if (m_changeQueued) return;
        m_changeQueued = true;
        const auto path = m_path;
        QMetaObject::invokeMethod(this, [this, path] {
            m_changeQueued = false;
            if (m_watchChanges && path == m_path) emit fileChanged();
        }, Qt::QueuedConnection);
    }
    QString m_path;
    QByteArray m_bytes;
    QPointer<JsonAdapter> m_adapter;
    QFutureWatcher<FileResult> m_watcher;
    std::optional<FileJob> m_current;
    std::optional<FileJob> m_pendingWrite;
    QPointer<QFileSystemWatcher> m_fileWatcher;
    quint64 m_generation = 0;
    bool m_preload = true;
    bool m_blockLoading = false;
    bool m_blockWrites = false;
    bool m_atomicWrites = true;
    bool m_printErrors = true;
    bool m_watchChanges = false;
    bool m_loaded = false;
    bool m_prepared = false;
    bool m_readRequested = false;
    bool m_complete = false;
    bool m_destroying = false;
    bool m_observedExists = false;
    bool m_changeQueued = false;
};

class DataStreamParser : public QObject {
    Q_OBJECT
public:
    using QObject::QObject;
    virtual void reset() = 0;
    virtual bool feed(const QByteArray &bytes) = 0;
    virtual void finish() = 0;
signals:
    void read(QString data);
};

class SplitParser : public DataStreamParser {
    Q_OBJECT
    Q_PROPERTY(QString splitMarker READ splitMarker WRITE setSplitMarker NOTIFY splitMarkerChanged)
public:
    using DataStreamParser::DataStreamParser;
    QString splitMarker() const { return QString::fromUtf8(m_marker); }
    void setSplitMarker(const QString &marker) {
        const auto bytes = marker.toUtf8();
        if (bytes == m_marker) return;
        m_marker = bytes;
        emit splitMarkerChanged();
    }
    void reset() override { m_buffer.clear(); }
    bool feed(const QByteArray &bytes) override {
        if (m_buffer.size() + bytes.size() > streamLimit) return false;
        m_buffer.append(bytes);
        const QPointer<SplitParser> guard(this);
        if (m_marker.isEmpty()) {
            const auto data = std::exchange(m_buffer, {});
            if (!data.isEmpty()) emit read(QString::fromUtf8(data));
            return true;
        }
        qsizetype index;
        while (!m_marker.isEmpty() && (index = m_buffer.indexOf(m_marker)) >= 0) {
            const auto line = m_buffer.first(index);
            m_buffer.remove(0, index + m_marker.size());
            emit read(QString::fromUtf8(line));
            if (!guard) return false;
        }
        return true;
    }
    void finish() override {
        const QPointer<SplitParser> guard(this);
        if (!feed({}) || !guard) return;
        const auto tail = std::exchange(m_buffer, {});
        if (!tail.isEmpty()) emit read(QString::fromUtf8(tail));
    }
signals:
    void splitMarkerChanged();
private:
    QByteArray m_marker = "\n";
    QByteArray m_buffer;
};

class StdioCollector : public DataStreamParser {
    Q_OBJECT
    Q_PROPERTY(QString text READ text NOTIFY dataChanged)
    Q_PROPERTY(QByteArray data READ data NOTIFY dataChanged)
    Q_PROPERTY(bool waitForEnd READ waitForEnd WRITE setWaitForEnd NOTIFY waitForEndChanged)
public:
    using DataStreamParser::DataStreamParser;
    QString text() const { return QString::fromUtf8(m_data); }
    QByteArray data() const { return m_data; }
    bool waitForEnd() const { return m_waitForEnd; }
    void setWaitForEnd(bool value) {
        if (m_waitForEnd == value) return;
        m_waitForEnd = value;
        emit waitForEndChanged();
    }
    void reset() override { m_buffer.clear(); }
    bool feed(const QByteArray &bytes) override {
        if (m_buffer.size() + bytes.size() > streamLimit) return false;
        m_buffer.append(bytes);
        if (!m_waitForEnd) {
            m_data = m_buffer;
            emit dataChanged();
        }
        return true;
    }
    void finish() override {
        m_data = std::exchange(m_buffer, {});
        const QPointer<StdioCollector> guard(this);
        emit dataChanged();
        if (guard) emit streamFinished();
    }
signals:
    void dataChanged();
    void waitForEndChanged();
    void streamFinished();
private:
    QByteArray m_data;
    QByteArray m_buffer;
    bool m_waitForEnd = true;
};

class Process : public QObject, public QQmlParserStatus {
    Q_OBJECT
    Q_INTERFACES(QQmlParserStatus)
    Q_PROPERTY(QStringList command READ command WRITE setCommand NOTIFY commandChanged)
    Q_PROPERTY(bool running READ running WRITE setRunning NOTIFY runningChanged)
    Q_PROPERTY(bool stdinEnabled READ stdinEnabled WRITE setStdinEnabled NOTIFY stdinEnabledChanged)
    Q_PROPERTY(DataStreamParser *stdout READ stdoutParser WRITE setStdoutParser NOTIFY stdoutParserChanged)
    Q_PROPERTY(DataStreamParser *stderr READ stderrParser WRITE setStderrParser NOTIFY stderrParserChanged)
public:
    using QObject::QObject;
    ~Process() override {
        if (!m_process) return;
        auto *process = m_process;
        disconnect(process, nullptr, this, nullptr);
        process->setParent(nullptr);
        connect(process, &QProcess::finished, process, &QObject::deleteLater);
        connect(process, &QProcess::errorOccurred, process, [process](QProcess::ProcessError error) {
            if (error == QProcess::FailedToStart) process->deleteLater();
        });
        process->kill();
        if (process->state() == QProcess::NotRunning) process->deleteLater();
    }
    void classBegin() override {}
    void componentComplete() override {
        m_complete = true;
        QMetaObject::invokeMethod(this, [this] { start(); }, Qt::QueuedConnection);
    }
    QStringList command() const { return m_command; }
    void setCommand(const QStringList &command) {
        if (m_command == command) return;
        m_command = command;
        const QPointer<Process> guard(this);
        emit commandChanged();
        if (guard) start();
    }
    bool running() const { return m_process != nullptr || m_finishing; }
    void setRunning(bool value) {
        m_wanted = value;
        if (value) { start(); return; }
        if (!m_process) return;
        m_stopRequested = true;
        m_process->terminate();
    }
    bool stdinEnabled() const { return m_stdinEnabled; }
    void setStdinEnabled(bool value) {
        if (m_stdinEnabled == value) return;
        m_stdinEnabled = value;
        if (m_process && !value) m_process->closeWriteChannel();
        emit stdinEnabledChanged();
    }
    DataStreamParser *stdoutParser() const { return m_stdout; }
    DataStreamParser *stderrParser() const { return m_stderr; }
    void setStdoutParser(DataStreamParser *parser) {
        if (m_stdout == parser) return;
        if (m_stdout) disconnect(m_stdout, nullptr, this, nullptr);
        m_stdout = parser;
        if (parser) {
            if (!parser->parent()) parser->setParent(this);
            parser->reset();
            connect(parser, &QObject::destroyed, this, &Process::stdoutParserChanged);
        }
        emit stdoutParserChanged();
    }
    void setStderrParser(DataStreamParser *parser) {
        if (m_stderr == parser) return;
        if (m_stderr) disconnect(m_stderr, nullptr, this, nullptr);
        m_stderr = parser;
        if (parser) {
            if (!parser->parent()) parser->setParent(this);
            parser->reset();
            connect(parser, &QObject::destroyed, this, &Process::stderrParserChanged);
        }
        emit stderrParserChanged();
    }
    Q_INVOKABLE void write(const QString &data) {
        if (!m_process || !m_stdinEnabled) {
            qmlWarning(this) << "Process stdin is not open";
            return;
        }
        const auto bytes = data.toUtf8();
        if (m_process->bytesToWrite() + bytes.size() > writeBufferLimit ||
            m_process->write(bytes) != bytes.size()) {
            qmlWarning(this) << "Process stdin write failed or exceeded the 1 MiB buffer limit";
            m_overflow = true;
            m_process->kill();
        }
    }
signals:
    void commandChanged();
    void runningChanged();
    void stdinEnabledChanged();
    void stdoutParserChanged();
    void stderrParserChanged();
    void started();
    void exited(qint32 exitCode, QProcess::ExitStatus exitStatus);
private:
    void start() {
        if (!m_complete || m_process || m_finishing || !m_wanted || m_command.isEmpty()) return;
        m_wanted = false;
        m_overflow = false;
        m_stopRequested = false;
        if (m_stdout) m_stdout->reset();
        if (m_stderr) m_stderr->reset();
        auto *process = new QProcess(this);
        m_process = process;
        if (!m_stdout) process->setStandardOutputFile(QProcess::nullDevice());
        if (!m_stderr) process->setStandardErrorFile(QProcess::nullDevice());
        if (!m_stdinEnabled) process->setStandardInputFile(QProcess::nullDevice());
        connect(process, &QProcess::started, this, [this] {
            const QPointer<Process> guard(this);
            emit started();
            if (guard && m_process && m_stopRequested) m_process->terminate();
        });
        connect(process, &QProcess::readyReadStandardOutput, this, [this] { drain(QProcess::StandardOutput); });
        connect(process, &QProcess::readyReadStandardError, this, [this] { drain(QProcess::StandardError); });
        connect(process, &QProcess::finished, this, &Process::finish);
        connect(process, &QProcess::errorOccurred, this, [this](QProcess::ProcessError error) {
            if (error != QProcess::FailedToStart) return;
            qmlWarning(this) << "Cannot start " << m_command << ": " << m_process->errorString();
            finish(-1, QProcess::CrashExit);
        });
        const QPointer<Process> guard(this);
        emit runningChanged();
        if (!guard) return;
        process->start(localPath(m_command.first()), m_command.mid(1));
        if (guard && m_process == process && !m_stdinEnabled) process->closeWriteChannel();
    }
    void drain(QProcess::ProcessChannel channel) {
        if (!m_process) return;
        const QPointer<Process> guard(this);
        const QPointer<QProcess> process(m_process);
        process->setReadChannel(channel);
        while (process->bytesAvailable() > 0) {
            const auto bytes = process->read(chunkSize);
            if (m_overflow) continue;
            const QPointer<DataStreamParser> parser = channel == QProcess::StandardOutput ? m_stdout : m_stderr;
            if (parser && !parser->feed(bytes)) {
                if (!guard) return;
                qmlWarning(this) << "Process output exceeded the 16 MiB stream limit";
                m_overflow = true;
                process->kill();
                return;
            }
            if (!guard || m_process != process) return;
        }
    }
    void finish(qint32 code, QProcess::ExitStatus status) {
        if (!m_process || m_finishing) return;
        m_finishing = true;
        const QPointer<Process> guard(this);
        drain(QProcess::StandardOutput);
        if (!guard) return;
        drain(QProcess::StandardError);
        if (!guard) return;
        auto *process = std::exchange(m_process, nullptr);
        disconnect(process, nullptr, this, nullptr);
        process->deleteLater();
        if (m_overflow) { code = -1; status = QProcess::CrashExit; }
        if (m_stdout) m_stdout->finish();
        if (!guard) return;
        if (m_stderr) m_stderr->finish();
        if (!guard) return;
        m_finishing = false;
        emit exited(code, status);
        if (!guard) return;
        emit runningChanged();
        if (guard) start();
    }
    QStringList m_command;
    QPointer<DataStreamParser> m_stdout;
    QPointer<DataStreamParser> m_stderr;
    QProcess *m_process = nullptr;
    bool m_complete = false;
    bool m_wanted = false;
    bool m_stdinEnabled = false;
    bool m_overflow = false;
    bool m_finishing = false;
    bool m_stopRequested = false;
};

class Socket : public QObject, public QQmlParserStatus {
    Q_OBJECT
    Q_INTERFACES(QQmlParserStatus)
    Q_PROPERTY(QString path READ path WRITE setPath NOTIFY pathChanged)
    Q_PROPERTY(bool connected READ connected WRITE setConnected NOTIFY connectionStateChanged)
    Q_PROPERTY(DataStreamParser *parser READ parser WRITE setParser NOTIFY readerChanged)
public:
    explicit Socket(QObject *parent = nullptr) : QObject(parent), m_socket(this) {
        m_socket.setReadBufferSize(chunkSize);
        connect(&m_socket, &QLocalSocket::connected, this, [this] {
            if (m_parser) m_parser->reset();
            emit connectionStateChanged();
        });
        connect(&m_socket, &QLocalSocket::disconnected, this, [this] {
            const QPointer<Socket> guard(this);
            readAvailable();
            if (!guard) return;
            if (m_parser) m_parser->finish();
            if (guard) emit connectionStateChanged();
        });
        connect(&m_socket, &QLocalSocket::readyRead, this, &Socket::readAvailable);
        connect(&m_socket, &QLocalSocket::errorOccurred, this, &Socket::error);
    }
    ~Socket() override {
        disconnect(&m_socket, nullptr, this, nullptr);
        m_socket.abort();
    }
    void classBegin() override {}
    void componentComplete() override {
        m_complete = true;
        QMetaObject::invokeMethod(this, [this] { dial(); }, Qt::QueuedConnection);
    }
    QString path() const { return m_path; }
    void setPath(const QString &path) {
        if (m_path == path) return;
        const QPointer<Socket> guard(this);
        m_path = path;
        m_socket.abort();
        if (!guard) return;
        emit pathChanged();
        if (guard) dial();
    }
    bool connected() const { return m_socket.state() == QLocalSocket::ConnectedState; }
    void setConnected(bool value) {
        m_wanted = value;
        if (value) { dial(); return; }
        m_socket.disconnectFromServer();
    }
    DataStreamParser *parser() const { return m_parser; }
    void setParser(DataStreamParser *parser) {
        if (m_parser == parser) return;
        if (m_parser) disconnect(m_parser, nullptr, this, nullptr);
        m_parser = parser;
        if (parser) {
            if (!parser->parent()) parser->setParent(this);
            parser->reset();
            connect(parser, &QObject::destroyed, this, &Socket::readerChanged);
        }
        emit readerChanged();
    }
    Q_INVOKABLE void write(const QString &data) {
        if (!connected()) { emit error(QLocalSocket::OperationError); return; }
        const auto bytes = data.toUtf8();
        if (m_socket.bytesToWrite() + bytes.size() > streamLimit ||
            m_socket.write(bytes) != bytes.size()) {
            const QPointer<Socket> guard(this);
            emit error(QLocalSocket::SocketResourceError);
            if (guard) m_socket.abort();
        }
    }
    Q_INVOKABLE void flush() { m_socket.flush(); }
signals:
    void pathChanged();
    void connectionStateChanged();
    void readerChanged();
    void error(QLocalSocket::LocalSocketError error);
private:
    void dial() {
        if (!m_complete || !m_wanted || m_path.isEmpty() ||
            m_socket.state() != QLocalSocket::UnconnectedState) return;
        m_socket.connectToServer(localPath(m_path));
    }
    void readAvailable() {
        const QPointer<Socket> guard(this);
        while (m_socket.bytesAvailable() > 0) {
            const auto bytes = m_socket.read(chunkSize);
            if (m_parser && !m_parser->feed(bytes)) {
                if (!guard) return;
                emit error(QLocalSocket::SocketResourceError);
                if (guard) m_socket.abort();
                return;
            }
            if (!guard) return;
        }
    }
    QLocalSocket m_socket;
    QPointer<DataStreamParser> m_parser;
    QString m_path;
    bool m_complete = false;
    bool m_wanted = false;
};

void registerIoTypes() {
    qmlRegisterType<FileView>("Quickshell.Io", 1, 0, "FileView");
    qmlRegisterType<JsonAdapter>("Quickshell.Io", 1, 0, "JsonAdapter");
    qmlRegisterType<Process>("Quickshell.Io", 1, 0, "Process");
    qmlRegisterType<SplitParser>("Quickshell.Io", 1, 0, "SplitParser");
    qmlRegisterType<StdioCollector>("Quickshell.Io", 1, 0, "StdioCollector");
    qmlRegisterType<Socket>("Quickshell.Io", 1, 0, "Socket");
    qmlRegisterUncreatableType<DataStreamParser>("Quickshell.Io", 1, 0, "DataStreamParser", "Parser base class");
    qmlRegisterUncreatableType<FileViewError>("Quickshell.Io", 1, 0, "FileViewError", "Error codes");
}

#include "io.moc"
