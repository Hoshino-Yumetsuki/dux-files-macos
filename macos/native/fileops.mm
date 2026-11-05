#include "native.h"

#import <Foundation/Foundation.h>
#include <QFile>
#include <QFileInfo>
#include <copyfile.h>
#include <fcntl.h>
#include <sys/acl.h>
#include <sys/attr.h>
#include <unistd.h>
#include <sys/stat.h>
#include <cerrno>
#include <cstring>
#include <stdexcept>

QJsonValue fileops(const QString &method, const QJsonObject &params) {
    if (method != QStringLiteral("fileops.preserve"))
        throw std::runtime_error("NOTSUPPORTED: unknown file operation");
    const auto src = params.value(QStringLiteral("src")).toString();
    const auto dst = params.value(QStringLiteral("dst")).toString();
    if (!QFileInfo(src).isAbsolute() || !QFileInfo(dst).isAbsolute() || src.contains(QChar::Null) || dst.contains(QChar::Null))
        throw std::runtime_error("EINVAL: absolute paths are required");
    const auto from = QFile::encodeName(src);
    const auto to = QFile::encodeName(dst);
    struct stat source{}, before{};
    if (lstat(from.constData(), &source) != 0 || lstat(to.constData(), &before) != 0)
        throw std::runtime_error(std::string("EIO: ") + std::strerror(errno));
    if (source.st_flags & (UF_IMMUTABLE | SF_IMMUTABLE | UF_APPEND | SF_APPEND | SF_NOUNLINK))
        throw std::runtime_error("NOTSUPPORTED: immutable or append-only metadata cannot be safely committed");
    int result;
    if (S_ISDIR(source.st_mode)) {
        int in = open(from.constData(), O_RDONLY | O_NOFOLLOW);
        if (in < 0) throw std::runtime_error(std::string("EIO: ") + std::strerror(errno));
        int out = open(to.constData(), O_RDONLY | O_NOFOLLOW);
        if (out < 0) { int error = errno; close(in); throw std::runtime_error(std::string("EIO: ") + std::strerror(error)); }
        result = fcopyfile(in, out, nullptr, COPYFILE_METADATA);
        int error = errno;
        close(in);
        close(out);
        errno = error;
    } else {
        result = copyfile(from.constData(), to.constData(), nullptr, COPYFILE_METADATA | COPYFILE_NOFOLLOW);
    }
    // Ownership belongs to the creating user, rather than the source owner.
    if (result != 0)
        throw std::runtime_error(std::string("EIO: preserving metadata: ") + std::strerror(errno));
    struct stat after{};
    if (lstat(to.constData(), &after) != 0 ||
        ((before.st_uid != after.st_uid || before.st_gid != after.st_gid) &&
         lchown(to.constData(), before.st_uid, before.st_gid) != 0))
        throw std::runtime_error(std::string("EIO: retaining destination ownership: ") + std::strerror(errno));
    if (lstat(to.constData(), &after) != 0)
        throw std::runtime_error(std::string("EIO: ") + std::strerror(errno));
    if (source.st_birthtimespec.tv_sec != after.st_birthtimespec.tv_sec || source.st_birthtimespec.tv_nsec != after.st_birthtimespec.tv_nsec) {
        struct attrlist attributes{};
        attributes.bitmapcount = ATTR_BIT_MAP_COUNT;
        attributes.commonattr = ATTR_CMN_CRTIME;
        if (setattrlist(to.constData(), &attributes, &source.st_birthtimespec, sizeof(source.st_birthtimespec), FSOPT_NOFOLLOW) != 0 || lstat(to.constData(), &after) != 0)
            throw std::runtime_error(std::string("EIO: preserving creation time: ") + std::strerror(errno));
    }
    if ((source.st_mode & 07777) != (after.st_mode & 07777) || source.st_flags != after.st_flags ||
        source.st_birthtimespec.tv_sec != after.st_birthtimespec.tv_sec || source.st_birthtimespec.tv_nsec != after.st_birthtimespec.tv_nsec ||
        source.st_mtimespec.tv_sec != after.st_mtimespec.tv_sec || source.st_mtimespec.tv_nsec != after.st_mtimespec.tv_nsec)
        throw std::runtime_error("NOTSUPPORTED: destination did not preserve file metadata");
    acl_t sourceACL = acl_get_link_np(from.constData(), ACL_TYPE_EXTENDED);
    int sourceError = errno;
    acl_t destACL = acl_get_link_np(to.constData(), ACL_TYPE_EXTENDED);
    if (!sourceACL && sourceError != ENOENT) {
        if (destACL) acl_free(destACL);
        throw std::runtime_error(std::string("EIO: reading access control list: ") + std::strerror(sourceError));
    }
    if (sourceACL) {
        char *sourceText = acl_to_text(sourceACL, nullptr);
        char *destText = destACL ? acl_to_text(destACL, nullptr) : nullptr;
        bool equal = sourceText && destText && std::strcmp(sourceText, destText) == 0;
        if (sourceText) acl_free(sourceText);
        if (destText) acl_free(destText);
        acl_free(sourceACL);
        if (destACL) acl_free(destACL);
        if (!equal) throw std::runtime_error("NOTSUPPORTED: destination did not preserve the access control list");
    } else if (destACL) {
        acl_free(destACL);
    }
    return QJsonObject{};
}
