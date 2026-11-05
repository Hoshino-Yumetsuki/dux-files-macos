#include "../native/trash.mm"

#include <QCoreApplication>
#include <QTemporaryDir>

static void require(bool condition, const char *message) {
    if (!condition) qFatal("%s", message);
}

int main(int argc, char **argv) {
    QCoreApplication app(argc, argv);
    @autoreleasepool {
        QTemporaryDir root;
        require(root.isValid(), "Temporary directory must exist");
        const auto dir = root.filePath("trash");
        const auto records = root.filePath("records");
        require(QDir().mkpath(dir) && QDir().mkpath(records), "Fixture directories must exist");
        const QSet<QString> directories{dir};
        const auto original = root.filePath("original/file.txt");
        const auto path = dir + "/file.txt";
        QFile file(path);
        require(file.open(QIODevice::WriteOnly) && file.write("contents") == 8, "Fixture item must exist");
        file.close();
        const auto id = identity(status(path));
        const auto pending = records + "/pending-recovery.json";
        QFile journal(pending);
        require(journal.open(QIODevice::WriteOnly), "Pending journal must be writable");
        require(journal.setPermissions(QFileDevice::ReadOwner | QFileDevice::WriteOwner), "Pending journal must be private");
        const auto body = QJsonDocument(QJsonObject{{"originalPath", original}, {"identity", id}}).toJson();
        require(journal.write(body) == body.size(), "Pending journal must be saved");
        journal.close();
        const auto recovered = entry(records, path);
        require(recovered.value("originalPath").toString() == original, "Interrupted move must retain its original location");
        require(recovered.value("infoPath").toString() == pending, "Pending record must be identified for cleanup");
        const auto foreign = dir + "/foreign.txt";
        QFile other(foreign);
        require(other.open(QIODevice::WriteOnly), "Foreign item must exist");
        other.close();
        require(entry(records, foreign).value("originalPath").toString().isEmpty(), "Another item must not inherit the pending origin");
        restoreItem(records, {{"trashDir", dir}, {"name", "file.txt"}, {"identity", id}}, directories);
        QFile restored(original);
        require(restored.open(QIODevice::ReadOnly) && restored.readAll() == "contents", "Restore must return the recovered item to its original location");
        require(!QFile::exists(path) && !QFile::exists(pending), "Restore must clean up its item and pending journal");

        const auto otherID = identity(status(foreign));
        QFile second(pending);
        require(second.open(QIODevice::WriteOnly), "Second pending journal must be writable");
        require(second.setPermissions(QFileDevice::ReadOwner | QFileDevice::WriteOwner), "Second pending journal must be private");
        const auto secondBody = QJsonDocument(QJsonObject{{"originalPath", root.filePath("other")}, {"identity", otherID}}).toJson();
        require(second.write(secondBody) == secondBody.size(), "Second pending journal must be saved");
        second.close();
        removeItem(records, {{"trashDir", dir}, {"name", "foreign.txt"}, {"identity", otherID}}, directories);
        require(!QFile::exists(foreign) && !QFile::exists(pending), "Removal must clean up its item and pending journal");
    }
}
