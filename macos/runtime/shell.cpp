#include "shell.h"
#include "../native/native.h"

#include <stdexcept>
#include <cerrno>
#include <sys/stat.h>

#include <QClipboard>
#include <QAbstractListModel>
#include <QQmlListProperty>
#include <QCloseEvent>
#include <QDir>
#include <QFileInfo>
#include <QDesktopServices>
#include <QDirIterator>
#include <QFileSystemWatcher>
#include <QGuiApplication>
#include <QIcon>
#include <QJsonArray>
#include <QJsonDocument>
#include <QJsonObject>
#include <QMimeData>
#include <QMimeDatabase>
#include <QPainterPath>
#include <QProcess>
#include <QQmlEngine>
#include <QQuickImageProvider>
#include <QQuickItem>
#include <QQuickWindow>
#include <QScreen>
#include <QSurfaceFormat>
#include <QTimer>
#include <QUrl>
#include <QUrlQuery>
#include <QStyleHints>
#include <QThreadPool>
#include <QRunnable>
#include <QQuickAsyncImageProvider>
#include <QQuickTextureFactory>
#include <atomic>
#include <QtQml>

class ShellObject : public QObject {
    Q_OBJECT
    Q_CLASSINFO("DefaultProperty", "data")
    Q_PROPERTY(QQmlListProperty<QObject> data READ data)
public:
    using QObject::QObject;
    QQmlListProperty<QObject> data() {
        return {this, &m_children,
            [](QQmlListProperty<QObject> *list, QObject *child) {
                auto children = static_cast<QList<QObject *> *>(list->data);
                children->append(child);
                child->setParent(list->object);
            },
            [](QQmlListProperty<QObject> *list) -> qsizetype { return static_cast<QList<QObject *> *>(list->data)->size(); },
            [](QQmlListProperty<QObject> *list, qsizetype index) { return static_cast<QList<QObject *> *>(list->data)->at(index); },
            [](QQmlListProperty<QObject> *list) { static_cast<QList<QObject *> *>(list->data)->clear(); }};
    }
private:
    QList<QObject *> m_children;
};

class ShellScriptModel : public QAbstractListModel {
    Q_OBJECT
    Q_PROPERTY(QJSValue values READ values WRITE setValues NOTIFY valuesChanged)
    Q_PROPERTY(QJSValue keyFunction READ keyFunction WRITE setKeyFunction NOTIFY keyFunctionChanged)
    Q_PROPERTY(int count READ rowCount NOTIFY countChanged)
public:
    using QAbstractListModel::QAbstractListModel;
    int rowCount(const QModelIndex &parent = {}) const override { return parent.isValid() ? 0 : m_rows.size(); }
    QHash<int, QByteArray> roleNames() const override { return {{Qt::UserRole, "modelData"}}; }
    QVariant data(const QModelIndex &index, int role) const override {
        if (!index.isValid() || index.row() >= m_rows.size() || role != Qt::UserRole) return {};
        return m_rows.at(index.row()).toVariant(QJSValue::RetainJSObjects);
    }
    QJSValue values() const { return m_values; }
    QJSValue keyFunction() const { return m_keyFunction; }
    void setKeyFunction(QJSValue function) {
        m_keyFunction = std::move(function);
        setValues(m_values);
        emit keyFunctionChanged();
    }
    void setValues(QJSValue values) {
        m_values = std::move(values);
        QList<QJSValue> next;
        const auto length = m_values.isArray() ? m_values.property(QStringLiteral("length")).toUInt() : 0;
        for (quint32 i = 0; i < length; ++i) next.append(m_values.property(i));
        const auto oldCount = m_rows.size();
        // ponytail: option lists use linear key lookup; index keys if this ever backs large datasets.
        for (qsizetype i = 0; i < next.size(); ++i) {
            const auto key = keyOf(next.at(i));
            qsizetype match = i;
            while (match < m_rows.size() && !keyOf(m_rows.at(match)).strictlyEquals(key)) ++match;
            if (match == m_rows.size()) {
                beginInsertRows({}, i, i);
                m_rows.insert(i, next.at(i));
                endInsertRows();
                continue;
            }
            if (match != i) {
                beginMoveRows({}, match, match, {}, i);
                m_rows.move(match, i);
                endMoveRows();
            }
            if (m_rows.at(i).strictlyEquals(next.at(i))) continue;
            m_rows[i] = next.at(i);
            emit dataChanged(index(i), index(i), {Qt::UserRole});
        }
        if (m_rows.size() > next.size()) {
            beginRemoveRows({}, next.size(), m_rows.size() - 1);
            m_rows.erase(m_rows.begin() + next.size(), m_rows.end());
            endRemoveRows();
        }
        if (oldCount != m_rows.size()) emit countChanged();
        emit valuesChanged();
    }
signals:
    void valuesChanged();
    void keyFunctionChanged();
    void countChanged();
private:
    QJSValue keyOf(const QJSValue &value) const { return m_keyFunction.isCallable() ? m_keyFunction.call({value}) : value; }
    QJSValue m_values;
    QJSValue m_keyFunction;
    QList<QJSValue> m_rows;
};

class ShellScreen : public QObject {
    Q_OBJECT
    Q_PROPERTY(int width READ width NOTIFY changed)
    Q_PROPERTY(int height READ height NOTIFY changed)
    Q_PROPERTY(int x READ x NOTIFY changed)
    Q_PROPERTY(int y READ y NOTIFY changed)
    Q_PROPERTY(QString name READ name NOTIFY changed)
    Q_PROPERTY(qreal devicePixelRatio READ devicePixelRatio NOTIFY changed)
    Q_PROPERTY(QRect availableGeometry READ availableGeometry NOTIFY changed)
public:
    explicit ShellScreen(QObject *parent = nullptr) : QObject(parent) {}
    void setScreen(QScreen *screen) {
        if (m_screen == screen) return;
        if (m_screen) disconnect(m_screen, nullptr, this, nullptr);
        m_screen = screen;
        if (screen) {
            connect(screen, &QScreen::geometryChanged, this, &ShellScreen::changed);
            connect(screen, &QScreen::availableGeometryChanged, this, &ShellScreen::changed);
            connect(screen, &QScreen::physicalDotsPerInchChanged, this, &ShellScreen::changed);
        }
        emit changed();
    }
    int width() const { return m_screen ? m_screen->geometry().width() : 0; }
    int height() const { return m_screen ? m_screen->geometry().height() : 0; }
    int x() const { return m_screen ? m_screen->geometry().x() : 0; }
    int y() const { return m_screen ? m_screen->geometry().y() : 0; }
    QString name() const { return m_screen ? m_screen->name() : QString(); }
    qreal devicePixelRatio() const { return m_screen ? m_screen->devicePixelRatio() : 1; }
    QRect availableGeometry() const { return m_screen ? m_screen->availableGeometry() : QRect(); }
signals:
    void changed();
private:
    QPointer<QScreen> m_screen;
};

class ShellFormat : public QObject {
    Q_OBJECT
    Q_PROPERTY(bool opaque READ opaque WRITE setOpaque NOTIFY opaqueChanged)
public:
    explicit ShellFormat(QWindow *window) : QObject(window), m_window(window) {}
    bool opaque() const { return m_opaque; }
    void setOpaque(bool opaque) {
        if (m_opaque == opaque) return;
        m_opaque = opaque;
        auto format = m_window->requestedFormat();
        format.setAlphaBufferSize(opaque ? 0 : 8);
        m_window->setFormat(format);
        emit opaqueChanged();
    }
signals:
    void opaqueChanged();
private:
    QWindow *m_window;
    bool m_opaque = false;
};

class ShellWindow : public QQuickWindow, public QQmlParserStatus {
    Q_OBJECT
    Q_INTERFACES(QQmlParserStatus)
    Q_PROPERTY(bool visible READ windowVisible WRITE setWindowVisible NOTIFY visibleChanged)
    Q_PROPERTY(qreal implicitWidth READ implicitWidth WRITE setImplicitWidth NOTIFY implicitWidthChanged)
    Q_PROPERTY(qreal implicitHeight READ implicitHeight WRITE setImplicitHeight NOTIFY implicitHeightChanged)
    Q_PROPERTY(QSize minimumSize READ minimumSize WRITE setMinimumSize NOTIFY minimumSizeChanged)
    Q_PROPERTY(QSize maximumSize READ maximumSize WRITE setMaximumSize NOTIFY maximumSizeChanged)
    Q_PROPERTY(bool maximized READ maximized WRITE setMaximized NOTIFY maximizedChanged)
    Q_PROPERTY(bool minimized READ minimized WRITE setMinimized NOTIFY minimizedChanged)
    Q_PROPERTY(ShellFormat *surfaceFormat READ surfaceFormat CONSTANT)
    Q_PROPERTY(ShellScreen *screen READ shellScreen NOTIFY shellScreenChanged)
    Q_PROPERTY(QWindow *parentWindow READ transientParent WRITE setTransientParent NOTIFY transientParentChanged)
public:
    explicit ShellWindow(QWindow *parent = nullptr) : QQuickWindow(parent), m_format(this), m_screen(this) {
        auto format = requestedFormat();
        format.setAlphaBufferSize(8);
        setFormat(format);
        m_screen.setScreen(QWindow::screen());
        connect(this, &QWindow::screenChanged, this, [this](QScreen *screen) {
            m_screen.setScreen(screen);
            emit shellScreenChanged();
        });
        connect(this, &QWindow::windowStateChanged, this, [this](Qt::WindowState state) {
            if (state != Qt::WindowMinimized) m_restoreState = state;
            emit maximizedChanged();
            emit minimizedChanged();
        });
        connect(this, &QWindow::minimumWidthChanged, this, &ShellWindow::minimumSizeChanged);
        connect(this, &QWindow::minimumHeightChanged, this, &ShellWindow::minimumSizeChanged);
        connect(this, &QWindow::maximumWidthChanged, this, &ShellWindow::maximumSizeChanged);
        connect(this, &QWindow::maximumHeightChanged, this, &ShellWindow::maximumSizeChanged);
        connect(this, &QWindow::visibleChanged, this, [this](bool visible) {
            if (!visible) return;
            QTimer::singleShot(0, this, [this] {
                if (!isVisible()) return;
                DuxCocoa::configureWindow(this);
                if (type() != Qt::Popup && !(flags() & Qt::WindowDoesNotAcceptFocus))
                    DuxCocoa::activateWindow(this);
            });
        });
    }
    void classBegin() override { m_complete = false; }
    void componentComplete() override {
        m_complete = true;
        setWindowVisible(m_requestedVisible);
    }
    bool windowVisible() const { return m_complete ? isVisible() : m_requestedVisible; }
    void setWindowVisible(bool visible) {
        m_requestedVisible = visible;
        if (!m_complete) return;
        if (visible && !isVisible() && type() != Qt::Popup) placeWindow();
        QQuickWindow::setVisible(visible);
    }
    qreal implicitWidth() const { return m_implicitWidth; }
    qreal implicitHeight() const { return m_implicitHeight; }
    void setImplicitWidth(qreal value) {
        if (qFuzzyCompare(m_implicitWidth, value)) return;
        m_implicitWidth = value;
        if (!maximized() && !minimized()) setWidth(qMax(1, qRound(value)));
        emit implicitWidthChanged();
    }
    void setImplicitHeight(qreal value) {
        if (qFuzzyCompare(m_implicitHeight, value)) return;
        m_implicitHeight = value;
        if (!maximized() && !minimized()) setHeight(qMax(1, qRound(value)));
        emit implicitHeightChanged();
    }
    bool maximized() const { return windowState() == Qt::WindowMaximized; }
    bool minimized() const { return windowState() == Qt::WindowMinimized; }
    void setMaximized(bool value) {
        if (value == maximized()) return;
        setWindowState(value ? Qt::WindowMaximized : Qt::WindowNoState);
    }
    void setMinimized(bool value) {
        if (value == minimized()) return;
        setWindowState(value ? Qt::WindowMinimized : m_restoreState);
    }
    ShellFormat *surfaceFormat() { return &m_format; }
    ShellScreen *shellScreen() { return &m_screen; }
signals:
    void implicitWidthChanged();
    void implicitHeightChanged();
    void minimumSizeChanged();
    void maximumSizeChanged();
    void maximizedChanged();
    void minimizedChanged();
    void shellScreenChanged();
    void closed();
protected:
    void closeEvent(QCloseEvent *event) override {
        event->ignore();
        emit closed();
    }
private:
    void placeWindow() {
        create();
        const auto screen = QWindow::screen();
        if (!screen || windowState() != Qt::WindowNoState) return;
        const auto available = screen->availableGeometry();
        if (available.isEmpty()) return;
        const auto margins = frameMargins();
        const QSize frameSize(margins.left() + margins.right(), margins.top() + margins.bottom());
        resize(size().boundedTo((available.size() - frameSize).expandedTo(QSize(1, 1))));
        auto frame = frameGeometry();
        if (!m_placed)
            frame.moveCenter(transientParent() ? transientParent()->frameGeometry().center() : available.center());
        frame.moveLeft(qBound(available.left(), frame.left(), qMax(available.left(), available.right() - frame.width() + 1)));
        frame.moveTop(qBound(available.top(), frame.top(), qMax(available.top(), available.bottom() - frame.height() + 1)));
        setFramePosition(frame.topLeft());
        m_placed = true;
    }
    bool m_complete = true;
    bool m_requestedVisible = false;
    bool m_placed = false;
    Qt::WindowState m_restoreState = Qt::WindowNoState;
    qreal m_implicitWidth = 0;
    qreal m_implicitHeight = 0;
    ShellFormat m_format;
    ShellScreen m_screen;
};

class ShellQsWindowAttached : public QObject {
    Q_OBJECT
    Q_PROPERTY(QQuickWindow *window READ window NOTIFY windowChanged)
public:
    explicit ShellQsWindowAttached(QObject *target) : QObject(target), m_target(target) {
        if (auto item = qobject_cast<QQuickItem *>(target))
            connect(item, &QQuickItem::windowChanged, this, &ShellQsWindowAttached::windowChanged);
    }
    QQuickWindow *window() const {
        if (auto window = qobject_cast<QQuickWindow *>(m_target)) return window;
        auto item = qobject_cast<QQuickItem *>(m_target);
        return item ? item->window() : nullptr;
    }
    Q_INVOKABLE QPointF itemPosition(QQuickItem *item) const {
        if (!item || !window()) return {};
        if (item->window() == window()) return item->mapToScene(QPointF());
        return window()->mapFromGlobal(item->mapToGlobal(QPointF()));
    }
signals:
    void windowChanged();
private:
    QObject *m_target;
};

class ShellQsWindow : public QObject {
    Q_OBJECT
public:
    static ShellQsWindowAttached *qmlAttachedProperties(QObject *target) { return new ShellQsWindowAttached(target); }
};
QML_DECLARE_TYPEINFO(ShellQsWindow, QML_HAS_ATTACHED_PROPERTIES)

class ShellIntersection : public QObject {
    Q_OBJECT
public:
    enum Mode { Combine, Subtract, Intersect, Xor };
    Q_ENUM(Mode)
};

class ShellRegion : public QQuickItem {
    Q_OBJECT
    Q_PROPERTY(qreal radius READ radius WRITE setRadius NOTIFY changed)
    Q_PROPERTY(int intersection READ intersection WRITE setIntersection NOTIFY changed)
public:
    explicit ShellRegion(QQuickItem *parent = nullptr) : QQuickItem(parent) {
        connect(this, &QQuickItem::xChanged, this, &ShellRegion::changed);
        connect(this, &QQuickItem::yChanged, this, &ShellRegion::changed);
        connect(this, &QQuickItem::widthChanged, this, &ShellRegion::changed);
        connect(this, &QQuickItem::heightChanged, this, &ShellRegion::changed);
    }
    qreal radius() const { return m_radius; }
    void setRadius(qreal value) { if (m_radius != value) { m_radius = value; emit changed(); } }
    int intersection() const { return m_intersection; }
    void setIntersection(int value) { if (m_intersection != value) { m_intersection = value; emit changed(); } }
    QPainterPath path() const {
        QPainterPath result;
        if (width() > 0 && height() > 0)
            result.addRoundedRect(QRectF(x(), y(), width(), height()), m_radius, m_radius);
        for (auto item : childItems()) {
            auto region = qobject_cast<ShellRegion *>(item);
            if (!region) continue;
            const auto child = region->path();
            switch (region->intersection()) {
            case ShellIntersection::Subtract: result = result.subtracted(child); break;
            case ShellIntersection::Intersect: result = result.intersected(child); break;
            case ShellIntersection::Xor: result = result.united(child).subtracted(result.intersected(child)); break;
            default: result = result.united(child); break;
            }
        }
        return result;
    }
signals:
    void changed();
protected:
    void itemChange(ItemChange change, const ItemChangeData &data) override {
        QQuickItem::itemChange(change, data);
        if (change != ItemChildAddedChange && change != ItemChildRemovedChange) return;
        auto region = qobject_cast<ShellRegion *>(data.item);
        if (!region) return;
        if (change == ItemChildAddedChange) connect(region, &ShellRegion::changed, this, &ShellRegion::changed);
        else disconnect(region, nullptr, this, nullptr);
        emit changed();
    }
private:
    qreal m_radius = 0;
    int m_intersection = ShellIntersection::Combine;
};

class ShellBackgroundAttached : public QObject {
    Q_OBJECT
    Q_PROPERTY(ShellRegion *blurRegion READ blurRegion WRITE setBlurRegion NOTIFY blurRegionChanged)
public:
    explicit ShellBackgroundAttached(QObject *target) : QObject(target), m_window(qobject_cast<QWindow *>(target)) {
        if (m_window) m_window->installEventFilter(this);
    }
    ShellRegion *blurRegion() const { return m_region; }
    void setBlurRegion(ShellRegion *region) {
        if (m_region == region) return;
        if (m_region) disconnect(m_region, nullptr, this, nullptr);
        m_region = region;
        if (region) {
            connect(region, &ShellRegion::changed, this, &ShellBackgroundAttached::apply);
            connect(region, &QObject::destroyed, this, [this] { m_region = nullptr; apply(); });
        }
        apply();
        emit blurRegionChanged();
    }
signals:
    void blurRegionChanged();
protected:
    bool eventFilter(QObject *, QEvent *event) override {
        switch (event->type()) {
        case QEvent::Show:
        case QEvent::Resize:
        case QEvent::PlatformSurface:
            QTimer::singleShot(0, this, &ShellBackgroundAttached::apply);
            break;
        default: break;
        }
        return false;
    }
private:
    void apply() {
        if (!m_window || !m_window->handle()) return;
        DuxCocoa::setBlur(m_window, m_region ? m_region->path() : QPainterPath());
    }
    QPointer<QWindow> m_window;
    QPointer<ShellRegion> m_region;
};

class ShellBackgroundEffect : public QObject {
    Q_OBJECT
public:
    static ShellBackgroundAttached *qmlAttachedProperties(QObject *target) { return new ShellBackgroundAttached(target); }
};
QML_DECLARE_TYPEINFO(ShellBackgroundEffect, QML_HAS_ATTACHED_PROPERTIES)

class ShellEdges : public QObject {
    Q_OBJECT
public:
    enum Edge { Top = Qt::TopEdge, Bottom = Qt::BottomEdge, Left = Qt::LeftEdge, Right = Qt::RightEdge };
    Q_ENUM(Edge)
};
class ShellPopupAdjustment : public QObject {
    Q_OBJECT
public:
    enum Adjustment { None = 0 };
    Q_ENUM(Adjustment)
};

class ShellMargins : public QObject {
    Q_OBJECT
    Q_PROPERTY(qreal top MEMBER top NOTIFY changed)
    Q_PROPERTY(qreal bottom MEMBER bottom NOTIFY changed)
    Q_PROPERTY(qreal left MEMBER left NOTIFY changed)
    Q_PROPERTY(qreal right MEMBER right NOTIFY changed)
public:
    using QObject::QObject;
    qreal top = 0;
    qreal bottom = 0;
    qreal left = 0;
    qreal right = 0;
signals:
    void changed();
};


class ShellPopupAnchor : public QObject {
    Q_OBJECT
    Q_PROPERTY(QWindow *window READ window WRITE setWindow NOTIFY changed)
    Q_PROPERTY(QRectF rect READ rect WRITE setRect NOTIFY changed)
    Q_PROPERTY(int edges MEMBER m_edges NOTIFY changed)
    Q_PROPERTY(int gravity MEMBER m_gravity NOTIFY changed)
    Q_PROPERTY(ShellMargins *margins READ margins CONSTANT)
    Q_PROPERTY(int adjustment MEMBER m_adjustment NOTIFY changed)
public:
    explicit ShellPopupAnchor(QWindow *popup) : QObject(popup), m_popup(popup), m_margins(this) {
        connect(this, &ShellPopupAnchor::changed, this, &ShellPopupAnchor::updateAnchor);
        connect(&m_margins, &ShellMargins::changed, this, &ShellPopupAnchor::updateAnchor);
    }
    QWindow *window() const { return m_window; }
    ShellMargins *margins() { return &m_margins; }
    void setWindow(QWindow *window) {
        if (m_window == window) return;
        if (m_window) disconnect(m_window, nullptr, this, nullptr);
        m_window = window;
        m_popup->setTransientParent(window);
        if (window) {
            connect(window, &QWindow::xChanged, this, &ShellPopupAnchor::updateAnchor);
            connect(window, &QWindow::yChanged, this, &ShellPopupAnchor::updateAnchor);
        }
        emit changed();
    }
    QRectF rect() const { return m_rect; }
    void setRect(QRectF rect) { if (m_rect != rect) { m_rect = rect; emit changed(); } }
    Q_INVOKABLE void updateAnchor() {
        if (!m_window) return;
        QPointF point = m_rect.topLeft();
        if (m_edges & Qt::RightEdge) point.rx() += m_rect.width();
        if (m_edges & Qt::BottomEdge) point.ry() += m_rect.height();
        if (m_gravity & Qt::LeftEdge) point.rx() -= m_popup->width();
        if (m_gravity & Qt::TopEdge) point.ry() -= m_popup->height();
        point.rx() += m_margins.left - m_margins.right;
        point.ry() += m_margins.top - m_margins.bottom;
        m_popup->setPosition(m_window->mapToGlobal(point.toPoint()));
    }
signals:
    void changed();
private:
    QWindow *m_popup;
    QPointer<QWindow> m_window;
    QRectF m_rect;
    int m_edges = Qt::TopEdge | Qt::LeftEdge;
    int m_gravity = Qt::BottomEdge | Qt::RightEdge;
    ShellMargins m_margins;
    int m_adjustment = 0;
};

class ShellPopupWindow : public ShellWindow {
    Q_OBJECT
    Q_PROPERTY(ShellPopupAnchor *anchor READ anchor CONSTANT)
    Q_PROPERTY(bool grabFocus READ grabFocus WRITE setGrabFocus NOTIFY grabFocusChanged)
public:
    ShellPopupWindow() : m_anchor(this) {
        setFlags(Qt::Popup | Qt::FramelessWindowHint);
        setColor(Qt::transparent);
        connect(this, &QWindow::widthChanged, &m_anchor, &ShellPopupAnchor::updateAnchor);
        connect(this, &QWindow::heightChanged, &m_anchor, &ShellPopupAnchor::updateAnchor);
        connect(this, &QWindow::visibleChanged, this, [this](bool visible) {
            if (!visible) return;
            m_anchor.updateAnchor();
            if (m_grabFocus) requestActivate();
        });
    }
    ShellPopupAnchor *anchor() { return &m_anchor; }
    bool grabFocus() const { return m_grabFocus; }
    void setGrabFocus(bool value) {
        if (value == m_grabFocus) return;
        m_grabFocus = value;
        setFlag(Qt::WindowDoesNotAcceptFocus, !value);
        emit grabFocusChanged();
    }
signals:
    void grabFocusChanged();
protected:
    void closeEvent(QCloseEvent *event) override { event->accept(); }
private:
    ShellPopupAnchor m_anchor;
    bool m_grabFocus = true;
};

class ShellToplevel : public QObject {
    Q_OBJECT
    Q_PROPERTY(QString appId READ appId CONSTANT)
    Q_PROPERTY(QString title READ title NOTIFY changed)
    Q_PROPERTY(bool activated READ activated NOTIFY changed)
    Q_PROPERTY(bool minimized READ minimized WRITE setMinimized NOTIFY changed)
public:
    ShellToplevel(QWindow *window, QObject *parent) : QObject(parent), m_window(window) {
        connect(window, &QWindow::windowTitleChanged, this, &ShellToplevel::changed);
        connect(window, &QWindow::activeChanged, this, &ShellToplevel::changed);
        connect(window, &QWindow::windowStateChanged, this, &ShellToplevel::changed);
    }
    QString appId() const { return QStringLiteral("dev.duxlabs.files"); }
    QString title() const { return m_window ? m_window->title() : QString(); }
    bool activated() const { return m_window && m_window->isActive(); }
    bool minimized() const { return m_window && m_window->windowState() == Qt::WindowMinimized; }
    void setMinimized(bool value) {
        if (!m_window) return;
        m_window->setWindowState(value ? Qt::WindowMinimized : Qt::WindowNoState);
    }
    Q_INVOKABLE void activate() {
        if (!m_window) return;
        if (minimized()) setMinimized(false);
        m_window->show();
        m_window->raise();
        DuxCocoa::activateWindow(m_window);
    }
signals:
    void changed();
private:
    QPointer<QWindow> m_window;
};

class ShellToplevelList : public QObject {
    Q_OBJECT
    Q_PROPERTY(QVariantList values READ values NOTIFY valuesChanged)
public:
    using QObject::QObject;
    QVariantList values() const { return m_values; }
    void setValues(QVariantList values) {
        if (m_values == values) return;
        m_values = std::move(values);
        emit valuesChanged();
    }
signals:
    void valuesChanged();
private:
    QVariantList m_values;
};

class ShellToplevelManager : public QObject {
    Q_OBJECT
    Q_PROPERTY(ShellToplevelList *toplevels READ toplevels CONSTANT)
    Q_PROPERTY(QObject *activeToplevel READ activeToplevel NOTIFY activeToplevelChanged)
public:
    explicit ShellToplevelManager(QObject *parent = nullptr) : QObject(parent), m_list(this) {
        qApp->installEventFilter(this);
        connect(qGuiApp, &QGuiApplication::focusWindowChanged, this, [this] { refresh(); });
        refresh();
    }
    ShellToplevelList *toplevels() { return &m_list; }
    QObject *activeToplevel() const { return m_active; }
signals:
    void activeToplevelChanged();
protected:
    bool eventFilter(QObject *object, QEvent *event) override {
        if (!qobject_cast<QWindow *>(object)) return false;
        switch (event->type()) {
        case QEvent::Show:
        case QEvent::Hide:
        case QEvent::Close:
            QTimer::singleShot(0, this, &ShellToplevelManager::refresh);
            break;
        default: break;
        }
        return false;
    }
private:
    void refresh() {
        QVariantList values;
        QObject *active = nullptr;
        const auto windows = QGuiApplication::allWindows();
        for (auto window : windows) {
            if (!qobject_cast<ShellWindow *>(window) || qobject_cast<ShellPopupWindow *>(window)) continue;
            if (!m_windows.contains(window)) {
                auto entry = new ShellToplevel(window, this);
                m_windows.insert(window, entry);
                connect(window, &QObject::destroyed, this, [this, window, entry] {
                    m_windows.remove(window);
                    if (m_active == entry) { m_active = nullptr; emit activeToplevelChanged(); }
                    entry->deleteLater();
                    refresh();
                });
            }
            if (!window->isVisible()) continue;
            auto entry = m_windows.value(window);
            values.append(QVariant::fromValue(static_cast<QObject *>(entry)));
            if (window == QGuiApplication::focusWindow()) active = entry;
        }
        m_list.setValues(values);
        if (m_active == active) return;
        m_active = active;
        emit activeToplevelChanged();
    }
    ShellToplevelList m_list;
    QHash<QWindow *, ShellToplevel *> m_windows;
    QPointer<QObject> m_active;
};

class ShellPlatform : public QObject {
    Q_OBJECT
    Q_PROPERTY(bool available READ available NOTIFY appearanceChanged)
    Q_PROPERTY(int colorScheme READ colorScheme NOTIFY appearanceChanged)
    Q_PROPERTY(bool hasClipboardImage READ hasClipboardImage NOTIFY clipboardImageChanged)
    Q_PROPERTY(QVariantList finderPlaces READ finderPlaces NOTIFY finderPlacesChanged)
    Q_PROPERTY(QString finderPlacesError READ finderPlacesError NOTIFY finderPlacesChanged)
public:
    explicit ShellPlatform(QObject *parent = nullptr) : QObject(parent) {
        connect(QGuiApplication::styleHints(), &QStyleHints::colorSchemeChanged, this, &ShellPlatform::appearanceChanged);
        connect(QGuiApplication::clipboard(), &QClipboard::dataChanged, this, &ShellPlatform::clipboardImageChanged);
    }
    ~ShellPlatform() override {
        const auto processes = findChildren<QProcess *>(QString(), Qt::FindDirectChildrenOnly);
        for (auto *process : processes) {
            process->disconnect();
            if (process->state() != QProcess::NotRunning) process->kill();
        }
        for (auto *process : processes) {
            if (process->state() != QProcess::NotRunning) process->waitForFinished(1000);
        }
    }
    bool hasClipboardImage() const {
        const auto data = QGuiApplication::clipboard()->mimeData();
        return data && data->hasImage();
    }
    bool available() const { return colorScheme() != 0; }
    int colorScheme() const {
        switch (QGuiApplication::styleHints()->colorScheme()) {
        case Qt::ColorScheme::Dark: return 1;
        case Qt::ColorScheme::Light: return 2;
        default: return 0;
        }
    }
    QVariantList finderPlaces() const { return m_finderPlaces; }
    QString finderPlacesError() const { return m_finderPlacesError; }
    Q_INVOKABLE void refreshFinderPlaces() {
        if (m_finderProcess) { m_finderRefreshPending = true; return; }
        auto process = new QProcess(this);
        m_finderProcess = process;
        auto finish = [this, process](bool success) {
            if (m_finderProcess != process) return;
            const auto reply = QJsonDocument::fromJson(process->readAllStandardOutput()).object();
            if (success && reply.value(QStringLiteral("entries")).isArray()) {
                m_finderPlaces = reply.value(QStringLiteral("entries")).toArray().toVariantList();
                m_finderPlacesError.clear();
            } else {
                m_finderPlacesError = reply.value(QStringLiteral("error")).toString(QStringLiteral("Finder shared lists are unavailable"));
            }
            m_finderProcess = nullptr;
            process->deleteLater();
            emit finderPlacesChanged();
            if (m_finderRefreshPending) { m_finderRefreshPending = false; refreshFinderPlaces(); }
        };
        connect(process, &QProcess::started, process, [process] {
            process->write("{\"method\":\"places.list\",\"params\":{}}\n");
            process->closeWriteChannel();
        });
        connect(process, &QProcess::errorOccurred, process, [finish](QProcess::ProcessError error) {
            if (error == QProcess::FailedToStart) finish(false);
        });
        connect(process, qOverload<int, QProcess::ExitStatus>(&QProcess::finished), process,
                [finish](int code, QProcess::ExitStatus status) { finish(status == QProcess::NormalExit && code == 0); });
        QTimer::singleShot(15000, process, [process] { if (process->state() != QProcess::NotRunning) process->kill(); });
        process->start(QCoreApplication::applicationDirPath() + QStringLiteral("/dfiles-native"));
    }
    Q_INVOKABLE void request(const QString &method, const QJSValue &params, const QJSValue &callback) {
        if (!callback.isCallable()) return;
        auto *engine = qobject_cast<QQmlEngine *>(parent());
        if (!engine) return;
        if (!params.isObject() || params.isArray() || method.isEmpty()) {
            callback.call({engine->toScriptValue(QVariantMap{{"code", "EINVAL"}, {"error", "Method and object parameters are required"}})});
            return;
        }
        const auto payload = QJsonDocument(QJsonObject{{"method", method}, {"params", QJsonObject::fromVariantMap(params.toVariant().toMap())}}).toJson(QJsonDocument::Compact);
        if (payload.size() > 16 * 1024 * 1024 || findChildren<QProcess *>(QString(), Qt::FindDirectChildrenOnly).size() >= 32) {
            callback.call({engine->toScriptValue(QVariantMap{{"code", "EBUSY"}, {"error", "Native request is too large or too many requests are running"}})});
            return;
        }
        auto *process = new QProcess(this);
        auto *timer = new QTimer(process);
        timer->setSingleShot(true);
        auto finish = [engine, process, callback](bool timedOut) {
            if (process->property("completed").toBool()) return;
            process->setProperty("completed", true);
            const auto data = process->readAllStandardOutput();
            QJsonParseError error;
            const auto document = QJsonDocument::fromJson(data, &error);
            QVariant result;
            if (!timedOut && data.size() <= 64 * 1024 * 1024 && error.error == QJsonParseError::NoError && !document.isNull())
                result = document.toVariant();
            else
                result = QVariantMap{{"code", timedOut ? "ETIMEDOUT" : "EIO"}, {"error", timedOut ? "Native request timed out" : "Native request failed"}};
            callback.call({engine->toScriptValue(result)});
            process->deleteLater();
        };
        connect(process, &QProcess::started, process, [process, payload] { process->write(payload); process->closeWriteChannel(); });
        connect(process, &QProcess::readyReadStandardOutput, process, [process] { if (process->bytesAvailable() > 64 * 1024 * 1024) process->kill(); });
        connect(process, &QProcess::errorOccurred, process, [finish](QProcess::ProcessError error) { if (error == QProcess::FailedToStart) finish(false); });
        connect(process, qOverload<int, QProcess::ExitStatus>(&QProcess::finished), process, [finish](int, QProcess::ExitStatus) { finish(false); });
        connect(timer, &QTimer::timeout, process, [process, finish] { process->kill(); finish(true); });
        timer->start(method == QStringLiteral("cloud.download") ? 300000 : method == QStringLiteral("apps.setDefault") || method == QStringLiteral("apps.folderHandler.set") ? 125000 : 30000);
        process->start(QCoreApplication::applicationDirPath() + QStringLiteral("/dfiles-native"));
    }
    Q_INVOKABLE bool previewFiles(QWindow *window, const QStringList &paths) { return DuxCocoa::previewFiles(window, paths); }
    Q_INVOKABLE bool shareFiles(QWindow *window, const QStringList &paths) { return DuxCocoa::shareFiles(window, paths); }
    Q_INVOKABLE bool openFinderPlace(const QString &url) const {
        return QDesktopServices::openUrl(QUrl(url));
    }
    Q_INVOKABLE QUrl localUrl(const QString &path) const { return QUrl::fromLocalFile(path); }
    Q_INVOKABLE bool isPackage(const QString &path) const { return DuxCocoa::isPackage(path); }
    Q_INVOKABLE bool localContent(const QString &path) const {
        if (!QDir::isAbsolutePath(path) || path.contains(QChar::Null)) return false;
        struct stat info;
        if (stat(path.toUtf8().constData(), &info) != 0) return errno == ENOENT || errno == ENOTDIR;
        return !(info.st_flags & SF_DATALESS);
    }
    Q_INVOKABLE QString thumbnailUrl(const QString &path) const {
        if (!QDir::isAbsolutePath(path) || path.contains(QChar::Null)) return {};
        const auto info = QFileInfo(path);
        return QStringLiteral("image://duxmedia/thumbnail?file=") + QString::fromLatin1(QUrl::toPercentEncoding(path))
            + QStringLiteral("&revision=") + QString::number(info.lastModified().toMSecsSinceEpoch()) + '-' + QString::number(info.size());
    }
    Q_INVOKABLE QStringList localPaths(const QList<QUrl> &urls) const {
        QStringList paths;
        for (const auto &url : urls) {
            const auto path = url.toLocalFile();
            if (!url.isValid() || !url.isLocalFile() || !url.host().isEmpty() || !QDir::isAbsolutePath(path) || path.contains(QChar::Null)) continue;
            paths.append(path);
        }
        return paths;
    }
signals:
    void finderPlacesChanged();
    void appearanceChanged();
    void clipboardImageChanged();
private:
    QVariantList m_finderPlaces;
    QString m_finderPlacesError;
    QProcess *m_finderProcess = nullptr;
    bool m_finderRefreshPending = false;
};

class ShellPdfDocument : public QObject {
    Q_OBJECT
    Q_PROPERTY(QUrl source READ source WRITE setSource NOTIFY sourceChanged)
    Q_PROPERTY(int status READ status NOTIFY statusChanged)
    Q_PROPERTY(QVariantList pages READ pages NOTIFY statusChanged)
    Q_PROPERTY(int pageCount READ pageCount NOTIFY statusChanged)
public:
    enum Status { Null, Loading, Ready, Error };
    Q_ENUM(Status)
    using QObject::QObject;
    QUrl source() const { return m_source; }
    int status() const { return m_status; }
    QVariantList pages() const { return m_pages; }
    int pageCount() const { return m_pages.size(); }
    void setSource(const QUrl &source) {
        if (source == m_source) return;
        m_source = source;
        m_pages.clear();
        m_status = source.isEmpty() ? Null : Loading;
        const auto revision = ++m_revision;
        emit sourceChanged();
        emit statusChanged();
        if (source.isEmpty()) return;
        QThreadPool::globalInstance()->start([source, revision, guard = QPointer<ShellPdfDocument>(this)] {
            const auto pages = DuxCocoa::pdfPages(source);
            QMetaObject::invokeMethod(qApp, [guard, pages, revision] {
                if (!guard || revision != guard->m_revision) return;
                guard->m_pages = pages;
                guard->m_status = pages.isEmpty() ? Error : Ready;
                emit guard->statusChanged();
            }, Qt::QueuedConnection);
        });
    }
    Q_INVOKABLE QString pageSource(int page, int rotation) const {
        if (m_status != Ready || page < 0 || page >= m_pages.size()) return {};
        return QStringLiteral("image://duxmedia/pdf?file=") + QString::fromLatin1(QUrl::toPercentEncoding(m_source.toLocalFile()))
            + QStringLiteral("&page=") + QString::number(page) + QStringLiteral("&rotation=") + QString::number(rotation);
    }
signals:
    void sourceChanged();
    void statusChanged();
private:
    QUrl m_source;
    QVariantList m_pages;
    int m_status = Null;
    quint64 m_revision = 0;
};

class ShellMediaResponse : public QQuickImageResponse {
public:
    ShellMediaResponse(const QString &id, QSize requested) {
        QThreadPool::globalInstance()->start([this, id, requested] {
            const QUrl request(QStringLiteral("image://duxmedia/") + id);
            const QUrlQuery query(request);
            const auto source = QUrl::fromLocalFile(query.queryItemValue(QStringLiteral("file"), QUrl::FullyDecoded));
            if (!m_cancelled) {
                m_image = request.path() == QStringLiteral("/pdf")
                    ? DuxCocoa::renderPdfPage(source, query.queryItemValue(QStringLiteral("page")).toInt(), query.queryItemValue(QStringLiteral("rotation")).toInt(), requested)
                    : DuxCocoa::thumbnail(source, requested);
            }
            emit finished();
        });
    }
    void cancel() override { m_cancelled = true; }
    QQuickTextureFactory *textureFactory() const override { return QQuickTextureFactory::textureFactoryForImage(m_image); }
    QString errorString() const override { return m_image.isNull() ? QStringLiteral("Native media could not be opened") : QString(); }
private:
    std::atomic_bool m_cancelled = false;
    QImage m_image;
};

class ShellMediaProvider : public QQuickAsyncImageProvider {
public:
    QQuickImageResponse *requestImageResponse(const QString &id, const QSize &requested) override { return new ShellMediaResponse(id, requested); }
};

class ShellIconResponse : public QQuickImageResponse {
public:
    ShellIconResponse(const QString &id, QSize requested) {
        moveToThread(qGuiApp->thread());
        QMetaObject::invokeMethod(this, [this, id, requested] {
            if (!m_cancelled) {
                const auto name = QUrl::fromPercentEncoding(id.toUtf8());
                const auto icon = QDir::isAbsolutePath(name) ? QIcon() : QIcon::fromTheme(name);
                m_image = icon.isNull() ? DuxCocoa::fileIcon(name, requested) : icon.pixmap(requested.isValid() ? requested : QSize(64, 64)).toImage();
            }
            emit finished();
        }, Qt::QueuedConnection);
    }
    void cancel() override { m_cancelled = true; }
    QQuickTextureFactory *textureFactory() const override { return QQuickTextureFactory::textureFactoryForImage(m_image); }
    QString errorString() const override { return m_image.isNull() ? QStringLiteral("Native icon could not be opened") : QString(); }
private:
    std::atomic_bool m_cancelled = false;
    QImage m_image;
};

class ShellIconProvider : public QQuickAsyncImageProvider {
public:
    QQuickImageResponse *requestImageResponse(const QString &id, const QSize &requested) override { return new ShellIconResponse(id, requested); }
};

class ShellQuickshell : public QObject {
    Q_OBJECT
    Q_PROPERTY(QString clipboardText READ clipboardText WRITE setClipboardText NOTIFY clipboardTextChanged)
    Q_PROPERTY(QVariantList screens READ screens NOTIFY screensChanged)
    Q_PROPERTY(bool watchFiles READ watchFiles WRITE setWatchFiles NOTIFY watchFilesChanged)
public:
    ShellQuickshell(QString root, QQmlEngine *engine) : QObject(engine), m_root(std::move(root)), m_engine(engine) {
        connect(QGuiApplication::clipboard(), &QClipboard::dataChanged, this, &ShellQuickshell::clipboardTextChanged);
        connect(qGuiApp, &QGuiApplication::screenAdded, this, [this] { refreshScreens(); });
        connect(qGuiApp, &QGuiApplication::screenRemoved, this, [this] { refreshScreens(); });
        refreshScreens();
        m_reload.setSingleShot(true);
        m_reload.setInterval(100);
        connect(&m_files, &QFileSystemWatcher::fileChanged, this, [this](const QString &path) {
            if (QFileInfo::exists(path)) m_files.addPath(path);
            m_reload.start();
        });
        connect(&m_reload, &QTimer::timeout, this, [this] { m_engine->clearComponentCache(); emit filesChanged(); });
    }
    Q_INVOKABLE QString env(const QString &name) const { return qEnvironmentVariable(name.toUtf8().constData()); }
    QString clipboardText() const { return QGuiApplication::clipboard()->text(); }
    void setClipboardText(const QString &text) { QGuiApplication::clipboard()->setText(text); }
    Q_INVOKABLE bool copyFiles(const QStringList &paths, bool cut) {
        if (paths.isEmpty()) return false;
        if (QGuiApplication::platformName() == QStringLiteral("cocoa")) {
            try {
                ::clipboard(QStringLiteral("clipboard.copy"), {{"paths", QJsonArray::fromStringList(paths)}, {"cut", cut}});
                emit clipboardTextChanged();
                return true;
            } catch (const std::exception &error) {
                qWarning() << "Could not copy file selection:" << error.what();
                return false;
            }
        }
        QList<QUrl> urls;
        for (const auto &path : paths) {
            if (!QDir::isAbsolutePath(path) || path.contains(QChar::Null)) return false;
            urls.append(QUrl::fromLocalFile(path));
        }
        auto data = new QMimeData;
        data->setUrls(urls);
        m_fileSelection = data;
        m_cutFiles = cut;
        QGuiApplication::clipboard()->setMimeData(data);
        return true;
    }
    Q_INVOKABLE QVariantMap readFiles() const {
        if (QGuiApplication::platformName() == QStringLiteral("cocoa")) {
            try {
                return ::clipboard(QStringLiteral("clipboard.paths"), {}).toObject().toVariantMap();
            } catch (const std::exception &error) {
                qWarning() << "Could not read file selection:" << error.what();
                return {{"paths", QStringList{}}, {"cut", false}, {"source", QString()}};
            }
        }
        const auto data = QGuiApplication::clipboard()->mimeData();
        QStringList paths;
        if (data) {
            for (const auto &url : data->urls()) {
                const auto path = url.toLocalFile();
                if (!url.isValid() || !url.isLocalFile() || !QDir::isAbsolutePath(path) || path.contains(QChar::Null)) continue;
                paths.append(path);
            }
        }
        const bool mine = data && data == m_fileSelection.data();
        return {{QStringLiteral("paths"), paths},
                {QStringLiteral("cut"), mine && m_cutFiles && !paths.isEmpty()},
                {QStringLiteral("source"), paths.isEmpty() ? QString() : mine ? QStringLiteral("dfiles") : QStringLiteral("text/uri-list")}};
    }
    Q_INVOKABLE void clearFiles() {
        if (QGuiApplication::platformName() == QStringLiteral("cocoa")) {
            try {
                ::clipboard(QStringLiteral("clipboard.clear"), {});
                emit clipboardTextChanged();
            } catch (const std::exception &error) {
                qWarning() << "Could not clear file selection:" << error.what();
            }
            return;
        }
        const auto data = QGuiApplication::clipboard()->mimeData();
        const bool mine = data && data == m_fileSelection.data();
        m_fileSelection.clear();
        m_cutFiles = false;
        if (mine) QGuiApplication::clipboard()->clear();
    }
    QVariantList screens() const { return m_screens; }
    bool watchFiles() const { return m_watchFiles; }
    void setWatchFiles(bool enabled) {
        if (m_watchFiles == enabled) return;
        m_watchFiles = enabled;
        if (!m_files.files().isEmpty()) m_files.removePaths(m_files.files());
        if (enabled) {
            QDirIterator iterator(m_root, {QStringLiteral("*.qml"), QStringLiteral("*.js")}, QDir::Files, QDirIterator::Subdirectories);
            QStringList paths;
            while (iterator.hasNext()) paths.append(iterator.next());
            if (!paths.isEmpty()) m_files.addPaths(paths);
        }
        emit watchFilesChanged();
    }
    Q_INVOKABLE bool execDetached(const QStringList &command) const {
        if (command.isEmpty() || command.first().isEmpty()) return false;
        return QProcess::startDetached(command.first(), command.mid(1));
    }
    Q_INVOKABLE QString iconPath(const QString &name, bool allowMissing = false) const {
        if (name.isEmpty()) return {};
        if (name.startsWith(QStringLiteral("/"))) return QUrl::fromLocalFile(name).toString();
        const QUrl url(name);
        if (!url.scheme().isEmpty()) return name;
        const auto bundled = QDir(m_root).filePath(QStringLiteral("assets/icons/") + name + QStringLiteral(".svg"));
        if (QFileInfo::exists(bundled)) return QUrl::fromLocalFile(bundled).toString();
        QString mime = name;
        const auto separator = mime.indexOf(QLatin1Char('-'));
        if (!mime.contains(QLatin1Char('/')) && separator >= 0) mime[separator] = QLatin1Char('/');
        static const QStringList nativeFolders{QStringLiteral("folder"), QStringLiteral("folder-documents"),
            QStringLiteral("folder-download"), QStringLiteral("folder-music"), QStringLiteral("folder-pictures"),
            QStringLiteral("folder-publicshare"), QStringLiteral("folder-remote"), QStringLiteral("folder-templates"),
            QStringLiteral("folder-videos")};
        const bool nativeIcon = nativeFolders.contains(name) || name == QStringLiteral("inode-directory") || name.startsWith(QStringLiteral("drive-")) ||
            name == QStringLiteral("user-trash") || name == QStringLiteral("user-trash-full") ||
            QMimeDatabase().mimeTypeForName(mime).isValid();
        if (allowMissing && !QIcon::hasThemeIcon(name) && !nativeIcon) return {};
        return QStringLiteral("image://duxicons/") + QString::fromLatin1(QUrl::toPercentEncoding(name));
    }
signals:
    void clipboardTextChanged();
    void screensChanged();
    void watchFilesChanged();
    void filesChanged();
private:
    void refreshScreens() {
        const auto native = QGuiApplication::screens();
        for (auto screen : native) {
            if (m_screenObjects.contains(screen)) continue;
            auto object = new ShellScreen(this);
            object->setScreen(screen);
            m_screenObjects.insert(screen, object);
        }
        for (auto iterator = m_screenObjects.begin(); iterator != m_screenObjects.end();) {
            if (native.contains(iterator.key())) { ++iterator; continue; }
            iterator.value()->deleteLater();
            iterator = m_screenObjects.erase(iterator);
        }
        m_screens.clear();
        for (auto screen : native) m_screens.append(QVariant::fromValue(static_cast<QObject *>(m_screenObjects.value(screen))));
        emit screensChanged();
    }
    QString m_root;
    QQmlEngine *m_engine;
    QVariantList m_screens;
    QHash<QScreen *, ShellScreen *> m_screenObjects;
    QPointer<QMimeData> m_fileSelection;
    bool m_cutFiles = false;
    QFileSystemWatcher m_files;
    QTimer m_reload;
    bool m_watchFiles = false;
};

void registerShellTypes(QQmlEngine *engine, const QString &shellRoot) {
    const QUrl helpers = QFile::exists(QStringLiteral(":/dux/runtime/LazyLoader.qml"))
        ? QUrl(QStringLiteral("qrc:/dux/runtime/"))
        : QUrl::fromLocalFile(QFileInfo(QString::fromUtf8(__FILE__)).absolutePath() + QStringLiteral("/qml/"));
    qmlRegisterType<ShellObject>("Quickshell", 1, 0, "Singleton");
    qmlRegisterType<ShellObject>("Quickshell", 1, 0, "Scope");
    qmlRegisterType<ShellScriptModel>("Quickshell", 1, 0, "ScriptModel");
    qmlRegisterType<QQuickItem>("Quickshell", 1, 0, "ShellRoot");
    qmlRegisterType<ShellWindow>("Quickshell", 1, 0, "FloatingWindow");
    qmlRegisterType<ShellPopupWindow>("Quickshell", 1, 0, "PopupWindow");
    qmlRegisterType<ShellRegion>("Quickshell", 1, 0, "Region");
    qmlRegisterType(helpers.resolved(QUrl(QStringLiteral("LazyLoader.qml"))), "Quickshell", 1, 0, "LazyLoader");
    qmlRegisterType(helpers.resolved(QUrl(QStringLiteral("IconImage.qml"))), "Quickshell.Widgets", 1, 0, "IconImage");
    qmlRegisterType(helpers.resolved(QUrl(QStringLiteral("ClippingRectangle.qml"))), "Quickshell.Widgets", 1, 0, "ClippingRectangle");
    qmlRegisterUncreatableType<ShellQsWindow>("Quickshell", 1, 0, "QsWindow", QStringLiteral("Attached property"));
    qmlRegisterUncreatableType<ShellIntersection>("Quickshell", 1, 0, "Intersection", QStringLiteral("Enum"));
    qmlRegisterUncreatableType<ShellEdges>("Quickshell", 1, 0, "Edges", QStringLiteral("Enum"));
    qmlRegisterUncreatableType<ShellPopupAdjustment>("Quickshell", 1, 0, "PopupAdjustment", QStringLiteral("Enum"));
    qmlRegisterUncreatableType<ShellBackgroundEffect>("Quickshell.Wayland", 1, 0, "BackgroundEffect", QStringLiteral("Attached property"));
    qmlRegisterAnonymousType<ShellScreen>("Quickshell", 1);
    qmlRegisterAnonymousType<ShellFormat>("Quickshell", 1);
    qmlRegisterAnonymousType<ShellPopupAnchor>("Quickshell", 1);
    qmlRegisterSingletonInstance("Quickshell", 1, 0, "Quickshell", new ShellQuickshell(shellRoot, engine));
    qmlRegisterSingletonInstance("Quickshell.Wayland", 1, 0, "ToplevelManager", new ShellToplevelManager(engine));
    qmlRegisterSingletonInstance("Quickshell", 1, 0, "NativePlatform", new ShellPlatform(engine));
    qmlRegisterType<ShellPdfDocument>("Quickshell", 1, 0, "NativePdfDocument");
    engine->addImageProvider(QStringLiteral("duxicons"), new ShellIconProvider);
    engine->addImageProvider(QStringLiteral("duxmedia"), new ShellMediaProvider);
}

#include "shell.moc"
