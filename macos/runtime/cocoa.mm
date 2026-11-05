#include "shell.h"

#include <QGuiApplication>
#include <QPainterPath>
#include <QWindow>
#include <QFileOpenEvent>
#include <QFileInfo>
#include <QDir>
#include <QMimeDatabase>
#include <QJSValue>
#include <QPointer>
#include <QEventLoop>
#include <QTimer>
#include <atomic>
#include <memory>
#import <AppKit/AppKit.h>
#import <AVFoundation/AVFoundation.h>
#import <CoreGraphics/CoreGraphics.h>
#import <ImageIO/ImageIO.h>
#import <QuickLookThumbnailing/QuickLookThumbnailing.h>
#import <Quartz/Quartz.h>
#import <QuartzCore/QuartzCore.h>
#import <UniformTypeIdentifiers/UniformTypeIdentifiers.h>
#import <objc/runtime.h>
#include <sys/stat.h>

@interface DuxVisualEffectView : NSVisualEffectView
@end

@implementation DuxVisualEffectView
- (NSView *)hitTest:(NSPoint)point {
    return nil;
}
@end

namespace {
char effectKey;
void installDesktopResponder(QWindow *window, NSView *view);

NSView *nativeView(QWindow *window) {
    if (!window || !window->handle() || QGuiApplication::platformName() != QStringLiteral("cocoa")) return nil;
    return (__bridge NSView *)(reinterpret_cast<void *>(window->winId()));
}

CGPathRef nativePath(const QPainterPath &path, CGFloat height) {
    CGMutablePathRef result = CGPathCreateMutable();
    for (int i = 0; i < path.elementCount(); ++i) {
        const auto element = path.elementAt(i);
        switch (element.type) {
        case QPainterPath::MoveToElement:
            CGPathMoveToPoint(result, nullptr, element.x, height - element.y);
            break;
        case QPainterPath::LineToElement:
            CGPathAddLineToPoint(result, nullptr, element.x, height - element.y);
            break;
        case QPainterPath::CurveToElement: {
            const auto control = path.elementAt(++i);
            const auto end = path.elementAt(++i);
            CGPathAddCurveToPoint(result, nullptr, element.x, height - element.y, control.x, height - control.y, end.x, height - end.y);
            break;
        }
        case QPainterPath::CurveToDataElement:
            break;
        }
    }
    return result;
}
}

void DuxCocoa::configureWindow(QWindow *window) {
    NSView *view = nativeView(window);
    if (!view || !view.window) return;
    NSWindow *native = view.window;
    native.opaque = NO;
    native.backgroundColor = NSColor.clearColor;
    native.hasShadow = YES;
    if (window->type() == Qt::Popup) return;
    installDesktopResponder(window, view);
    native.collectionBehavior |= NSWindowCollectionBehaviorFullScreenPrimary;
}

void DuxCocoa::activateWindow(QWindow *window) {
    if (!window) return;
    NSView *view = nativeView(window);
    if (view.window) {
        [NSApp activateIgnoringOtherApps:YES];
        [view.window makeKeyAndOrderFront:nil];
    }
    window->requestActivate();
}

void DuxCocoa::setBlur(QWindow *window, const QPainterPath &region) {
    NSView *view = nativeView(window);
    if (!view || !view.window) return;
    DuxVisualEffectView *effect = objc_getAssociatedObject(view, &effectKey);
    if (region.isEmpty()) {
        [effect removeFromSuperview];
        objc_setAssociatedObject(view, &effectKey, nil, OBJC_ASSOCIATION_RETAIN_NONATOMIC);
        return;
    }

    configureWindow(window);
    NSView *container = view.superview;
    if (!container) return;
    if (!effect) {
        effect = [[DuxVisualEffectView alloc] initWithFrame:view.frame];
        effect.autoresizingMask = NSViewWidthSizable | NSViewHeightSizable;
        effect.material = NSVisualEffectMaterialUnderWindowBackground;
        effect.blendingMode = NSVisualEffectBlendingModeBehindWindow;
        effect.state = NSVisualEffectStateFollowsWindowActiveState;
        effect.wantsLayer = YES;
        [container addSubview:effect positioned:NSWindowBelow relativeTo:view];
        objc_setAssociatedObject(view, &effectKey, effect, OBJC_ASSOCIATION_RETAIN_NONATOMIC);
    }
    if (effect.superview != container) [container addSubview:effect positioned:NSWindowBelow relativeTo:view];
    effect.frame = view.frame;
    CAShapeLayer *mask = [CAShapeLayer layer];
    mask.frame = effect.bounds;
    CGPathRef path = nativePath(region, effect.bounds.size.height);
    mask.path = path;
    mask.fillRule = region.fillRule() == Qt::OddEvenFill ? kCAFillRuleEvenOdd : kCAFillRuleNonZero;
    CGPathRelease(path);
    effect.layer.mask = mask;
}

namespace {
NSURL *mediaURL(const QUrl &source) {
    if (!source.isValid() || !source.isLocalFile() || !source.host().isEmpty() || !QDir::isAbsolutePath(source.toLocalFile()) || source.toLocalFile().contains(QChar::Null)) return nil;
    struct stat info;
    if (stat(source.toLocalFile().toUtf8().constData(), &info) != 0 || (info.st_flags & SF_DATALESS)) return nil;
    return [NSURL fileURLWithPath:source.toLocalFile().toNSString()];
}

QSize boundedSize(QSize requested) {
    if (!requested.isValid() || requested.isEmpty()) return QSize(512, 512);
    return requested.boundedTo(QSize(4096, 4096));
}

QImage fromCGImage(CGImageRef image) {
    if (!image) return {};
    QImage result(int(CGImageGetWidth(image)), int(CGImageGetHeight(image)), QImage::Format_RGBA8888_Premultiplied);
    if (result.isNull()) return {};
    CGColorSpaceRef colorSpace = CGColorSpaceCreateWithName(kCGColorSpaceSRGB);
    CGContextRef context = CGBitmapContextCreate(result.bits(), result.width(), result.height(), 8, result.bytesPerLine(), colorSpace, kCGImageAlphaPremultipliedLast | kCGBitmapByteOrder32Big);
    CGColorSpaceRelease(colorSpace);
    if (!context) return {};
    CGContextSetBlendMode(context, kCGBlendModeCopy);
    CGContextDrawImage(context, CGRectMake(0, 0, result.width(), result.height()), image);
    CGContextRelease(context);
    return result;
}
}

bool DuxCocoa::isPackage(const QString &path) {
    @autoreleasepool {
        if (!QDir::isAbsolutePath(path) || path.contains(QChar::Null)) return false;
        NSURL *url = [NSURL fileURLWithPath:path.toNSString()];
        id value = nil;
        return [url getResourceValue:&value forKey:NSURLIsPackageKey error:nil] && [(NSNumber *)value boolValue];
    }
}

QVariantList DuxCocoa::pdfPages(const QUrl &source) {
    @autoreleasepool {
        NSURL *url = mediaURL(source);
        if (!url) return {};
        CGPDFDocumentRef document = CGPDFDocumentCreateWithURL((__bridge CFURLRef)url);
        if (!document) return {};
        QVariantList pages;
        if (CGPDFDocumentIsUnlocked(document)) {
            for (size_t i = 1; i <= CGPDFDocumentGetNumberOfPages(document); ++i) {
                CGPDFPageRef page = CGPDFDocumentGetPage(document, i);
                CGRect box = CGPDFPageGetBoxRect(page, kCGPDFCropBox);
                QSizeF size(box.size.width, box.size.height);
                if (CGPDFPageGetRotationAngle(page) % 180) size.transpose();
                pages.append(QVariantMap{{QStringLiteral("width"), size.width()}, {QStringLiteral("height"), size.height()}});
            }
        }
        CGPDFDocumentRelease(document);
        return pages;
    }
}

QImage DuxCocoa::renderPdfPage(const QUrl &source, int pageNumber, int rotation, QSize requested) {
    @autoreleasepool {
        NSURL *url = mediaURL(source);
        if (!url || pageNumber < 0) return {};
        CGPDFDocumentRef document = CGPDFDocumentCreateWithURL((__bridge CFURLRef)url);
        if (!document) return {};
        CGPDFPageRef page = CGPDFDocumentGetPage(document, size_t(pageNumber) + 1);
        if (!page || !CGPDFDocumentIsUnlocked(document)) { CGPDFDocumentRelease(document); return {}; }
        CGRect box = CGPDFPageGetBoxRect(page, kCGPDFCropBox);
        QSizeF dimensions(box.size.width, box.size.height);
        rotation = ((rotation % 360) + 360) % 360;
        if ((CGPDFPageGetRotationAngle(page) + rotation) % 180) dimensions.transpose();
        const QSize pixels = dimensions.toSize().scaled(boundedSize(requested), Qt::KeepAspectRatio);
        QImage result(pixels, QImage::Format_RGBA8888_Premultiplied);
        result.fill(Qt::white);
        CGColorSpaceRef colorSpace = CGColorSpaceCreateWithName(kCGColorSpaceSRGB);
        CGContextRef context = result.isNull() ? nullptr : CGBitmapContextCreate(result.bits(), result.width(), result.height(), 8, result.bytesPerLine(), colorSpace, kCGImageAlphaPremultipliedLast | kCGBitmapByteOrder32Big);
        CGColorSpaceRelease(colorSpace);
        if (context) {
            CGContextConcatCTM(context, CGPDFPageGetDrawingTransform(page, kCGPDFCropBox, CGRectMake(0, 0, pixels.width(), pixels.height()), rotation, true));
            CGContextDrawPDFPage(context, page);
            CGContextRelease(context);
        }
        CGPDFDocumentRelease(document);
        return context ? result : QImage();
    }
}

QImage DuxCocoa::quickLookThumbnail(const QUrl &source, QSize requested) {
    @autoreleasepool {
        if (!source.isValid() || !source.isLocalFile() || !source.host().isEmpty() || !QDir::isAbsolutePath(source.toLocalFile()) || source.toLocalFile().contains(QChar::Null)) return {};
        requested = boundedSize(requested);
        NSURL *url = [NSURL fileURLWithPath:source.toLocalFile().toNSString()];
        auto types = static_cast<QLThumbnailGenerationRequestRepresentationTypes>(QLThumbnailGenerationRequestRepresentationTypeLowQualityThumbnail | QLThumbnailGenerationRequestRepresentationTypeThumbnail);
        QLThumbnailGenerationRequest *request = [[QLThumbnailGenerationRequest alloc] initWithFileAtURL:url size:CGSizeMake(requested.width(), requested.height()) scale:1.0 representationTypes:types];
        struct Result { QImage image; std::atomic<bool> done{false}; };
        auto result = std::make_shared<Result>();
        QLThumbnailGenerator *generator = QLThumbnailGenerator.sharedGenerator;
        [generator generateBestRepresentationForRequest:request completionHandler:^(QLThumbnailRepresentation *representation, NSError *) {
            @autoreleasepool {
                if (representation && representation.type != QLThumbnailRepresentationTypeIcon)
                    result->image = fromCGImage(representation.CGImage);
                result->done.store(true, std::memory_order_release);
            }
        }];
        QEventLoop loop;
        QTimer poll, deadline;
        QObject::connect(&poll, &QTimer::timeout, &loop, [&] { if (result->done.load(std::memory_order_acquire)) loop.quit(); });
        QObject::connect(&deadline, &QTimer::timeout, &loop, &QEventLoop::quit);
        poll.start(20);
        deadline.setSingleShot(true);
        deadline.start(15000);
        if (!result->done.load(std::memory_order_acquire)) loop.exec();
        if (!result->done.load(std::memory_order_acquire)) {
            [generator cancelRequest:request];
            return {};
        }
        return result->image.scaled(requested, Qt::KeepAspectRatio, Qt::SmoothTransformation);
    }
}

QImage DuxCocoa::thumbnail(const QUrl &source, QSize requested) {
    @autoreleasepool {
        if (!source.isValid() || !source.isLocalFile() || !source.host().isEmpty() || !QDir::isAbsolutePath(source.toLocalFile()) || source.toLocalFile().contains(QChar::Null)) return {};
        struct stat info;
        if (stat(source.toLocalFile().toUtf8().constData(), &info) != 0) return {};
        if (info.st_flags & SF_DATALESS) return quickLookThumbnail(source, requested);
        NSURL *url = mediaURL(source);
        if (!url) return {};
        requested = boundedSize(requested);
        CGImageSourceRef input = CGImageSourceCreateWithURL((__bridge CFURLRef)url, nullptr);
        if (input) {
            NSDictionary *options = @{(id)kCGImageSourceCreateThumbnailFromImageAlways: @YES,
                (id)kCGImageSourceCreateThumbnailWithTransform: @YES,
                (id)kCGImageSourceThumbnailMaxPixelSize: @(qMax(requested.width(), requested.height()))};
            CGImageRef image = CGImageSourceCreateThumbnailAtIndex(input, 0, (__bridge CFDictionaryRef)options);
            auto result = fromCGImage(image);
            if (image) CGImageRelease(image);
            CFRelease(input);
            if (!result.isNull()) return result;
        }
        const auto mime = QMimeDatabase().mimeTypeForFile(source.toLocalFile()).name();
        if (mime == QStringLiteral("application/pdf")) return renderPdfPage(source, 0, 0, requested);
        if (!mime.startsWith(QStringLiteral("video/")) && !mime.startsWith(QStringLiteral("audio/"))) return {};
        AVURLAsset *asset = [AVURLAsset URLAssetWithURL:url options:@{AVURLAssetPreferPreciseDurationAndTimingKey: @NO}];
        if (mime.startsWith(QStringLiteral("video/"))) {
            AVAssetImageGenerator *generator = [AVAssetImageGenerator assetImageGeneratorWithAsset:asset];
            generator.appliesPreferredTrackTransform = YES;
            generator.maximumSize = CGSizeMake(requested.width(), requested.height());
            CGImageRef image = [generator copyCGImageAtTime:kCMTimeZero actualTime:nullptr error:nil];
            auto result = fromCGImage(image);
            if (image) CGImageRelease(image);
            return result;
        }
        for (AVMetadataItem *item in asset.commonMetadata) {
            if (![item.commonKey isEqual:AVMetadataCommonKeyArtwork] || !item.dataValue) continue;
            CGImageSourceRef artwork = CGImageSourceCreateWithData((__bridge CFDataRef)item.dataValue, nullptr);
            if (!artwork) continue;
            NSDictionary *options = @{(id)kCGImageSourceCreateThumbnailFromImageAlways: @YES,
                (id)kCGImageSourceCreateThumbnailWithTransform: @YES,
                (id)kCGImageSourceThumbnailMaxPixelSize: @(qMax(requested.width(), requested.height()))};
            CGImageRef image = CGImageSourceCreateThumbnailAtIndex(artwork, 0, (__bridge CFDictionaryRef)options);
            auto result = fromCGImage(image);
            if (image) CGImageRelease(image);
            CFRelease(artwork);
            if (!result.isNull()) return result;
        }
        return {};
    }
}

QImage DuxCocoa::fileIcon(const QString &name, QSize requested) {
    @autoreleasepool {
        requested = boundedSize(requested);
        NSImage *icon = nil;
        if (QFileInfo(name).isAbsolute()) icon = [NSWorkspace.sharedWorkspace iconForFile:name.toNSString()];
        else if (name.startsWith(QStringLiteral("user-trash"))) icon = [NSImage imageNamed:name.contains(QStringLiteral("full")) ? NSImageNameTrashFull : NSImageNameTrashEmpty];
        else if (name == QStringLiteral("folder") || name.startsWith(QStringLiteral("folder-")) || name == QStringLiteral("inode-directory")) icon = [NSImage imageNamed:NSImageNameFolder];
        else if (name.startsWith(QStringLiteral("drive-"))) icon = [NSImage imageNamed:NSImageNameComputer];
        else {
            QString mime = name;
            const auto separator = mime.indexOf(QLatin1Char('-'));
            if (!mime.contains(QLatin1Char('/')) && separator >= 0) mime[separator] = QLatin1Char('/');
            const auto mimeType = QMimeDatabase().mimeTypeForName(mime);
            UTType *type = mimeType.isValid() ? [UTType typeWithMIMEType:mimeType.name().toNSString()] : nil;
            if (!type && !mimeType.preferredSuffix().isEmpty()) type = [UTType typeWithFilenameExtension:mimeType.preferredSuffix().toNSString()];
            if (type) icon = [NSWorkspace.sharedWorkspace iconForContentType:type];
            if (!icon) icon = [NSImage imageNamed:NSImageNameMultipleDocuments];
        }
        if (!icon) return {};
        NSRect rect = NSMakeRect(0, 0, requested.width(), requested.height());
        return fromCGImage([icon CGImageForProposedRect:&rect context:nil hints:nil]).scaled(requested, Qt::KeepAspectRatio, Qt::SmoothTransformation);
    }
}

@interface DuxPreviewResponder : NSResponder <QLPreviewPanelDataSource, QLPreviewPanelDelegate> {
@public
    QPointer<QWindow> window;
}
@property(nonatomic, strong) NSArray<NSURL *> *urls;
@end

@implementation DuxPreviewResponder
- (NSArray<NSURL *> *)serviceSelection {
    if (!window || window->property("textEditing").toBool() || window->property("dialogOpen").toBool()) return nil;
    QVariant value;
    if (!QMetaObject::invokeMethod(window.data(), "selectedPaths", Q_RETURN_ARG(QVariant, value))) return nil;
    if (value.metaType() == QMetaType::fromType<QJSValue>()) value = value.value<QJSValue>().toVariant();
    NSMutableArray<NSURL *> *urls = [NSMutableArray array];
    for (const QVariant &item : value.toList()) {
        const auto path = item.toString();
        if (!QDir::isAbsolutePath(path) || path.contains(QChar::Null)) return nil;
        [urls addObject:[NSURL fileURLWithPath:path.toNSString()]];
    }
    return urls.count ? urls : nil;
}
- (id)validRequestorForSendType:(NSPasteboardType)sendType returnType:(NSPasteboardType)returnType {
    if ((!returnType || returnType.length == 0) && ([sendType isEqualToString:NSPasteboardTypeFileURL] || [sendType isEqualToString:NSPasteboardTypeURL]) && self.serviceSelection.count)
        return self;
    return [super validRequestorForSendType:sendType returnType:returnType];
}
- (BOOL)writeSelectionToPasteboard:(NSPasteboard *)pasteboard types:(NSArray<NSPasteboardType> *)types {
    if ([pasteboard.name isEqualToString:NSPasteboardNameGeneral] || (![types containsObject:NSPasteboardTypeFileURL] && ![types containsObject:NSPasteboardTypeURL])) return NO;
    NSArray<NSURL *> *urls = self.serviceSelection;
    if (!urls.count) return NO;
    [pasteboard clearContents];
    return [pasteboard writeObjects:urls];
}
- (BOOL)acceptsPreviewPanelControl:(QLPreviewPanel *)panel { return self.urls.count > 0; }
- (void)beginPreviewPanelControl:(QLPreviewPanel *)panel { panel.dataSource = self; panel.delegate = self; }
- (void)endPreviewPanelControl:(QLPreviewPanel *)panel { panel.dataSource = nil; panel.delegate = nil; }
- (NSInteger)numberOfPreviewItemsInPreviewPanel:(QLPreviewPanel *)panel { return self.urls.count; }
- (id<QLPreviewItem>)previewPanel:(QLPreviewPanel *)panel previewItemAtIndex:(NSInteger)index { return self.urls[index]; }
@end

namespace {
char previewResponderKey;
char sharingPickerKey;

void installDesktopResponder(QWindow *window, NSView *view) {
    if (objc_getAssociatedObject(view, &previewResponderKey)) return;
    DuxPreviewResponder *responder = [[DuxPreviewResponder alloc] init];
    responder->window = window;
    responder.nextResponder = view.nextResponder;
    view.nextResponder = responder;
    objc_setAssociatedObject(view, &previewResponderKey, responder, OBJC_ASSOCIATION_RETAIN_NONATOMIC);
}

NSArray<NSURL *> *desktopURLs(const QStringList &paths, bool requireLocalContent) {
    NSMutableArray<NSURL *> *urls = [NSMutableArray array];
    for (const auto &path : paths) {
        if (!QDir::isAbsolutePath(path) || path.contains(QChar::Null)) return nil;
        struct stat info;
        if (stat(path.toUtf8().constData(), &info) != 0 || (requireLocalContent && (info.st_flags & SF_DATALESS))) return nil;
        [urls addObject:[NSURL fileURLWithPath:path.toNSString()]];
    }
    return urls.count ? urls : nil;
}
}

bool DuxCocoa::previewFiles(QWindow *window, const QStringList &paths) {
    NSView *view = nativeView(window);
    NSArray<NSURL *> *urls = desktopURLs(paths, true);
    if (!view.window || !urls) return false;
    installDesktopResponder(window, view);
    DuxPreviewResponder *responder = objc_getAssociatedObject(view, &previewResponderKey);
    responder.urls = urls;
    QLPreviewPanel *panel = [QLPreviewPanel sharedPreviewPanel];
    if (panel.visible && panel.dataSource == responder) {
        [panel orderOut:nil];
    } else {
        [view.window makeFirstResponder:view];
        [panel makeKeyAndOrderFront:nil];
        [panel reloadData];
    }
    return true;
}

bool DuxCocoa::shareFiles(QWindow *window, const QStringList &paths) {
    NSView *view = nativeView(window);
    NSArray<NSURL *> *urls = desktopURLs(paths, true);
    if (!view.window || !urls) return false;
    NSSharingServicePicker *picker = [[NSSharingServicePicker alloc] initWithItems:urls];
    objc_setAssociatedObject(view, &sharingPickerKey, picker, OBJC_ASSOCIATION_RETAIN_NONATOMIC);
    [picker showRelativeToRect:view.bounds ofView:view preferredEdge:NSMinYEdge];
    return true;
}

@interface DuxServicesProvider : NSObject
@end

@implementation DuxServicesProvider
- (void)openFiles:(NSPasteboard *)pasteboard userData:(NSString *)userData error:(NSString **)error {
    NSArray<NSURL *> *urls = [pasteboard readObjectsForClasses:@[[NSURL class]] options:@{NSPasteboardURLReadingFileURLsOnlyKey: @YES}];
    if (!urls.count) {
        if (error) *error = @"No local file URLs were supplied.";
        return;
    }
    for (NSURL *url in urls) {
        if (!url.isFileURL) continue;
        QCoreApplication::postEvent(qApp, new QFileOpenEvent(QUrl::fromLocalFile(QString::fromNSString(url.path))));
    }
}
@end

void DuxCocoa::registerServices() {
    static DuxServicesProvider *provider = [[DuxServicesProvider alloc] init];
    NSApp.servicesProvider = provider;
    NSUpdateDynamicServices();
}
