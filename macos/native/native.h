#pragma once

#include <QJsonObject>
#include <QJsonValue>
#include <QString>

QJsonValue apps(const QString &method, const QJsonObject &params);
QJsonValue clipboard(const QString &method, const QJsonObject &params);
QJsonValue volumes(const QString &method, const QJsonObject &params);
QJsonValue trash(const QString &method, const QJsonObject &params);
QJsonValue search(const QString &method, const QJsonObject &params);
QJsonValue places(const QString &method, const QJsonObject &params);
QJsonValue metadata(const QString &method, const QJsonObject &params);
QJsonValue cloud(const QString &method, const QJsonObject &params);
QJsonValue fileops(const QString &method, const QJsonObject &params);
int coordinateFileops();
void watchVolumes();
void publishEvent(const QString &topic, const QJsonValue &data = QJsonValue());
