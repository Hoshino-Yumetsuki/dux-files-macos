#include "../native/cloud.mm"
#include <QFile>
#include <QTemporaryDir>
#include <cassert>

int main() {
    @autoreleasepool {
        QTemporaryDir temporary;
        assert(temporary.isValid());
        const QString path = temporary.path() + QStringLiteral("/雪 # file");
        QFile file(path);
        assert(file.open(QIODevice::WriteOnly));
        assert(file.write("content") == 7);
        file.close();
        const QJsonObject params{{QStringLiteral("path"), path}};
        const auto before = cloud(QStringLiteral("cloud.status"), params).toObject();
        assert(before.value(QStringLiteral("path")).toString() == path);
        assert(before.value(QStringLiteral("state")).toString() == QStringLiteral("local"));
        const auto downloaded = cloud(QStringLiteral("cloud.download"), params).toObject();
        assert(downloaded.value(QStringLiteral("state")).toString() == QStringLiteral("local"));
        assert(file.open(QIODevice::ReadOnly));
        assert(file.readAll() == QByteArray("content"));
        file.close();
        bool missing = false;
        try { cloud(QStringLiteral("cloud.status"), {{QStringLiteral("path"), path + QStringLiteral(".missing")}}); }
        catch (const std::runtime_error &) { missing = true; }
        assert(missing);
        bool notDirectory = false;
        try { cloud(QStringLiteral("cloud.status"), {{QStringLiteral("path"), path + QStringLiteral("/child")}}); }
        catch (const std::runtime_error &error) { notDirectory = QByteArray(error.what()).startsWith("ENOTDIR: "); }
        assert(notDirectory);
        bool invalid = false;
        try { cloud(QStringLiteral("cloud.download"), {{QStringLiteral("path"), QStringLiteral("relative")}}); }
        catch (const std::runtime_error &) { invalid = true; }
        assert(invalid);
        bool unsupported = false;
        try { cloud(QStringLiteral("cloud.evict"), params); }
        catch (const std::runtime_error &) { unsupported = true; }
        assert(unsupported);
    }
}
