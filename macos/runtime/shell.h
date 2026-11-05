#pragma once

#include <QString>
#include <QImage>
#include <QUrl>
#include <QVariantList>

class QQmlEngine;
class QWindow;
class QPainterPath;

void registerShellTypes(QQmlEngine *engine, const QString &shellRoot);

namespace DuxCocoa {
void configureWindow(QWindow *window);
void activateWindow(QWindow *window);
void setBlur(QWindow *window, const QPainterPath &region);
bool isPackage(const QString &path);
QVariantList pdfPages(const QUrl &source);
QImage renderPdfPage(const QUrl &source, int page, int rotation, QSize requested);
QImage thumbnail(const QUrl &source, QSize requested);
QImage quickLookThumbnail(const QUrl &source, QSize requested);
QImage fileIcon(const QString &name, QSize requested);
bool previewFiles(QWindow *window, const QStringList &paths);
bool shareFiles(QWindow *window, const QStringList &paths);
void registerServices();
}
