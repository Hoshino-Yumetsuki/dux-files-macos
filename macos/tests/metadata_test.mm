#include "../native/metadata.mm"
#include "../native/fileops.mm"
#include <QFile>
#include <QFileInfo>
#include <QTemporaryDir>
#include <cassert>

int main() {
    @autoreleasepool {
        QTemporaryDir temporary;
        assert(temporary.isValid());
        const QString a = temporary.path() + "/first";
        const QString b = temporary.path() + "/second";
        QFile first(a), second(b);
        assert(first.open(QIODevice::WriteOnly)); first.close();
        assert(second.open(QIODevice::WriteOnly)); second.close();
        const QJsonArray both{a, b};
        auto edit = [&](QJsonArray paths, const char *mode, QJsonArray values) {
            return metadata("metadata.tagsEdit", {{"paths", paths}, {"mode", mode}, {"tags", values}}).toObject().value("errors").toArray();
        };
        assert(edit({a}, "add", {"existing"}).isEmpty());
        assert(edit(both, "add", {"shared"}).isEmpty());
        auto items = metadata("metadata.tagsRead", {{"paths", both}}).toObject().value("items").toArray();
        assert(items[0].toObject().value("tags").toArray().contains("existing"));
        assert(items[0].toObject().value("tags").toArray().contains("shared"));
        assert(!items[1].toObject().value("tags").toArray().contains("existing"));
        assert(edit(both, "remove", {"shared"}).isEmpty());
        items = metadata("metadata.tagsRead", {{"paths", both}}).toObject().value("items").toArray();
        assert(items[0].toObject().value("tags").toArray().contains("existing"));
        assert(!items[0].toObject().value("tags").toArray().contains("shared"));
        assert(edit({a}, "replace", {"replacement"}).isEmpty());
        items = metadata("metadata.tagsList", {{"path", temporary.path()}}).toObject().value("items").toArray();
        bool found = false;
        for (const auto &item : items) if (item.toObject().value("path").toString() == a)
            found = item.toObject().value("tags").toArray().contains("replacement");
        assert(found);
        const QString alias = temporary.path() + "/alias";
        metadata("metadata.aliasCreate", {{"path", a}, {"destination", alias}});
        assert(metadata("metadata.aliasInfo", {{"path", alias}}).toObject().value("isAlias").toBool());
        assert(QFileInfo(metadata("metadata.aliasResolve", {{"path", alias}}).toObject().value("path").toString()).canonicalFilePath() == QFileInfo(a).canonicalFilePath());
        bool exists = false;
        try { metadata("metadata.aliasCreate", {{"path", b}, {"destination", alias}}); }
        catch (const std::exception &e) { exists = QByteArray(e.what()).startsWith("EEXIST:"); }
        assert(exists);
        assert(QFileInfo(metadata("metadata.aliasResolve", {{"path", alias}}).toObject().value("path").toString()).canonicalFilePath() == QFileInfo(a).canonicalFilePath());
        const QString link = temporary.path() + "/link";
        assert(::symlink(QFile::encodeName(a).constData(), QFile::encodeName(link).constData()) == 0);
        assert(!metadata("metadata.aliasInfo", {{"path", link}}).toObject().value("isAlias").toBool());
        const QString sourceDir = temporary.path() + "/source-dir";
        const QString copiedDir = temporary.path() + "/copied-dir";
        assert(QDir().mkdir(sourceDir));
        assert(QDir().mkdir(copiedDir));
        struct attrlist attributes{};
        attributes.bitmapcount = ATTR_BIT_MAP_COUNT;
        attributes.commonattr = ATTR_CMN_CRTIME;
        struct timespec birth{1600000000, 123456789};
        assert(setattrlist(QFile::encodeName(sourceDir).constData(), &attributes, &birth, sizeof(birth), FSOPT_NOFOLLOW) == 0);
        fileops("fileops.preserve", {{"src", sourceDir}, {"dst", copiedDir}});
        struct stat sourceInfo{}, copiedInfo{};
        assert(lstat(QFile::encodeName(sourceDir).constData(), &sourceInfo) == 0);
        assert(lstat(QFile::encodeName(copiedDir).constData(), &copiedInfo) == 0);
        assert(sourceInfo.st_birthtimespec.tv_sec == copiedInfo.st_birthtimespec.tv_sec);
        assert(sourceInfo.st_birthtimespec.tv_nsec == copiedInfo.st_birthtimespec.tv_nsec);
        assert(sourceInfo.st_mtimespec.tv_sec == copiedInfo.st_mtimespec.tv_sec);
        assert(sourceInfo.st_mtimespec.tv_nsec == copiedInfo.st_mtimespec.tv_nsec);
    }
}
