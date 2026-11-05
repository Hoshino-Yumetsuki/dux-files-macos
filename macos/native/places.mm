#import <AppKit/AppKit.h>
#import <CoreServices/CoreServices.h>

#include <QJsonArray>
#include <QJsonObject>
#include <QSet>
#include <algorithm>
#include <stdexcept>
#include <utility>
#include <vector>

namespace {
id itemProperty(LSSharedFileListItemRef item, NSString *key) {
    return CFBridgingRelease(LSSharedFileListItemCopyProperty(item, (__bridge CFStringRef)key));
}

NSDictionary *finderLabels() {
    NSURL *url = [NSWorkspace.sharedWorkspace URLForApplicationWithBundleIdentifier:@"com.apple.finder"];
    NSBundle *bundle = [NSBundle bundleWithURL:url];
    NSString *locale = [NSBundle preferredLocalizationsFromArray:bundle.localizations forPreferences:NSLocale.preferredLanguages].firstObject;
    NSString *path = [bundle pathForResource:@"LocalizableMerged" ofType:@"strings" inDirectory:nil forLocalization:locale];
    return path ? [NSDictionary dictionaryWithContentsOfFile:path] : @{};
}

NSString *label(NSDictionary *labels, NSString *key, NSString *fallback) {
    id value = labels[key];
    return [value isKindOfClass:NSString.class] && [value length] ? value : fallback;
}

NSString *placeKind(NSURL *url, NSString *special) {
    if ([special hasSuffix:@".IsTrash"] || [url.absoluteString isEqualToString:@"x-finder:Trash"]) return @"trash";
    if ([special hasSuffix:@".IsICloudDrive"]) return @"icloud";
    if ([special hasSuffix:@".IsHome"]) return @"home";
    if ([url.absoluteString isEqualToString:@"nwnode://domain-AirDrop"]) return @"airdrop";
    if ([special isEqualToString:@"FileProvider"]) return @"provider";
    if ([url.path hasPrefix:@"/System/Library/CoreServices/Finder.app/Contents/Resources/MyLibraries/"]) {
        if ([url.lastPathComponent isEqualToString:@"myDocuments.cannedSearch"]) return @"recent";
        if ([url.lastPathComponent isEqualToString:@"SharedDocuments.cannedSearch"]) return @"shared";
    }
    return @"folder";
}

bool hiddenPlace(id hidden, id visibility, NSURL *url) {
    if ([hidden isKindOfClass:NSNumber.class] && [hidden boolValue]) return true;
    if ([visibility isEqual:@"NeverVisible"]) return true;
    return !url || [url.scheme isEqualToString:@"com-apple-sfl"];
}

QJsonObject placeEntry(NSString *name, NSURL *url, NSString *kind, const char *section, NSDictionary *labels) {
    NSString *path = url.isFileURL && !url.host.length ? url.path : @"";
    if ([kind isEqualToString:@"icloud"]) {
        name = label(labels, @"SD11", @"iCloud Drive");
        NSString *cloud = [NSHomeDirectoryForUser(NSUserName()) stringByAppendingPathComponent:@"Library/Mobile Documents/com~apple~CloudDocs"];
        BOOL directory = NO;
        if ([NSFileManager.defaultManager fileExistsAtPath:cloud isDirectory:&directory] && directory) path = cloud;
    }
    if ([kind isEqualToString:@"airdrop"]) name = label(labels, @"TL35", @"AirDrop");
    if ([kind isEqualToString:@"trash"]) name = label(labels, @"N39", @"Trash");
    if ([kind isEqualToString:@"shared"]) { name = label(labels, @"SD11.4", @"Shared"); path = @""; }
    if ([kind isEqualToString:@"recent"]) { name = label(labels, @"NE91", @"Recents"); path = @""; }
    if (!name.length && path.length) name = [NSFileManager.defaultManager displayNameAtPath:path];
    NSString *title = @"";
    if (strcmp(section, "favorites") == 0) title = label(labels, @"SD8.1", @"Favorites");
    if (strcmp(section, "locations") == 0) title = label(labels, @"SD5", @"Locations");
    return {{"section", QString::fromLatin1(section)}, {"sectionTitle", QString::fromNSString(title)},
            {"kind", QString::fromNSString(kind)}, {"label", name ? QString::fromNSString(name) : QString()},
            {"path", QString::fromNSString(path)}, {"url", QString::fromNSString(url.absoluteString)}};
}

NSImage *providerSymbol(NSString *identifier) {
    NSString *extensionID = [identifier componentsSeparatedByString:@"/"].firstObject;
    NSString *ownerID = extensionID;
    while (ownerID.length) {
        NSURL *app = [NSWorkspace.sharedWorkspace URLForApplicationWithBundleIdentifier:ownerID];
        if (app) {
            NSBundle *bundle = [NSBundle bundleWithURL:app];
            NSArray<NSURL *> *extensions = [NSFileManager.defaultManager contentsOfDirectoryAtURL:bundle.builtInPlugInsURL includingPropertiesForKeys:nil options:NSDirectoryEnumerationSkipsHiddenFiles error:nil];
            for (NSURL *extension in extensions) {
                NSBundle *plugin = [NSBundle bundleWithURL:extension];
                if (![plugin.bundleIdentifier isEqualToString:extensionID]) continue;
                NSString *symbol = plugin.infoDictionary[@"CFBundleIcons"][@"CFBundlePrimaryIcon"][@"CFBundleSymbolName"];
                if (![symbol isKindOfClass:NSString.class]) return nil;
                return [NSImage imageWithSymbolName:symbol bundle:plugin variableValue:0]
                    ?: [NSImage imageWithSystemSymbolName:symbol accessibilityDescription:nil];
            }
        }
        NSRange dot = [ownerID rangeOfString:@"." options:NSBackwardsSearch];
        if (dot.location == NSNotFound) break;
        ownerID = [ownerID substringToIndex:dot.location];
    }
    return nil;
}

NSString *systemSymbol(NSURL *url, NSString *kind, bool volume) {
    static NSDictionary *const symbols = @{@"icloud": @"icloud", @"home": @"house", @"airdrop": @"airdrop", @"trash": @"trash",
        @"recent": @"clock", @"shared": @"person.2", @"provider": @"cloud"};
    if (symbols[kind]) return symbols[kind];
    if (volume) return @"internaldrive";
    for (const auto &entry : {std::pair<NSSearchPathDirectory, NSString *>{NSApplicationDirectory, @"app.dashed"},
            {NSDesktopDirectory, @"menubar.dock.rectangle"}, {NSDocumentDirectory, @"doc"}, {NSDownloadsDirectory, @"arrow.down.circle"},
            {NSMusicDirectory, @"music.note"}, {NSPicturesDirectory, @"photo"}, {NSMoviesDirectory, @"film"}}) {
        for (NSURL *directory in [NSFileManager.defaultManager URLsForDirectory:entry.first inDomains:NSUserDomainMask | NSLocalDomainMask]) {
            if ([directory.path isEqualToString:url.path]) return entry.second;
        }
    }
    return @"folder";
}

QString symbolData(NSImage *image) {
    if (!image) return {};
    NSBitmapImageRep *bitmap = [[NSBitmapImageRep alloc] initWithBitmapDataPlanes:nullptr pixelsWide:64 pixelsHigh:64 bitsPerSample:8 samplesPerPixel:4 hasAlpha:YES isPlanar:NO colorSpaceName:NSCalibratedRGBColorSpace bytesPerRow:0 bitsPerPixel:0];
    if (!bitmap || image.size.width <= 0 || image.size.height <= 0) return {};
    const CGFloat scale = 64 / std::max(image.size.width, image.size.height);
    const NSSize size = NSMakeSize(image.size.width * scale, image.size.height * scale);
    [NSGraphicsContext saveGraphicsState];
    [NSGraphicsContext setCurrentContext:[NSGraphicsContext graphicsContextWithBitmapImageRep:bitmap]];
    [image drawInRect:NSMakeRect((64 - size.width) / 2, (64 - size.height) / 2, size.width, size.height)];
    [NSColor.whiteColor setFill];
    NSRectFillUsingOperation(NSMakeRect(0, 0, 64, 64), NSCompositingOperationSourceAtop);
    [NSGraphicsContext restoreGraphicsState];
    NSData *data = [bitmap representationUsingType:NSBitmapImageFileTypePNG properties:@{}];
    return data.length ? QStringLiteral("data:image/png;base64,") + QString::fromLatin1(QByteArray(static_cast<const char *>(data.bytes), data.length).toBase64()) : QString();
}

int locationGroup(NSString *kind, id group) {
    if ([kind isEqualToString:@"icloud"]) return -1;
    if ([kind isEqualToString:@"airdrop"]) return 6;
    if ([kind isEqualToString:@"trash"]) return 7;
    return [group isKindOfClass:NSNumber.class] ? [group intValue] : 0;
}
}

QJsonValue places(const QString &method, const QJsonObject &) {
    if (method != QStringLiteral("places.list"))
        throw std::runtime_error("NOTSUPPORTED: unknown Finder places method");
    NSDictionary *labels = finderLabels();
    QJsonArray entries;
    QSet<QString> destinations;
    for (const auto &source : {std::pair<CFStringRef, const char *>{CFSTR("com.apple.LSSharedFileList.TopSidebarSection"), "top"},
            {kLSSharedFileListFavoriteItems, "favorites"}, {kLSSharedFileListFavoriteVolumes, "locations"}}) {
        LSSharedFileListRef list = LSSharedFileListCreate(nullptr, source.first, nullptr);
        if (!list) throw std::runtime_error("EIO: Finder shared list is unavailable");
        NSArray *items = CFBridgingRelease(LSSharedFileListCopySnapshot(list, nullptr));
        CFRelease(list);
        if (!items) throw std::runtime_error("EIO: Finder shared list snapshot failed");
        std::vector<std::pair<int, QJsonObject>> rows;
        rows.reserve(items.count);
        for (id object in items) {
            auto item = (__bridge LSSharedFileListItemRef)object;
            id hidden = itemProperty(item, (__bridge NSString *)kLSSharedFileListItemHidden);
            id visibility = itemProperty(item, @"com.apple.LSSharedFileList.ItemVisibility");
            NSURL *url = CFBridgingRelease(LSSharedFileListItemCopyResolvedURL(item, kLSSharedFileListNoUserInteraction | kLSSharedFileListDoNotMountVolumes, nullptr));
            if (hiddenPlace(hidden, visibility, url)) continue;
            NSString *name = CFBridgingRelease(LSSharedFileListItemCopyDisplayName(item));
            NSString *kind = placeKind(url, itemProperty(item, @"com.apple.LSSharedFileList.SpecialItemIdentifier"));
            auto row = placeEntry(name, url, kind, source.second, labels);
            const auto destination = row.value("path").toString().isEmpty() ? row.value("url").toString() : row.value("path").toString();
            if (row.value("label").toString().isEmpty() || destinations.contains(destination)) continue;
            destinations.insert(destination);
            NSImage *image = [kind isEqualToString:@"provider"] ? providerSymbol(itemProperty(item, @"com.apple.finder.kLSSharedFileListItemBundleID")) : nil;
            NSString *resource = [kind isEqualToString:@"airdrop"] ? @"SidebarAirDrop" : [url.path isEqualToString:@"/Applications"] ? @"SidebarApplicationsFolder" : nil;
            if (!image && resource) {
                NSBundle *coreTypes = [NSBundle bundleWithPath:@"/System/Library/CoreServices/CoreTypes.bundle"];
                NSString *path = [coreTypes pathForResource:resource ofType:@"icns"];
                if (path) image = [[NSImage alloc] initWithContentsOfFile:path];
            }
            NSNumber *volume = nil;
            if (url.isFileURL) [url getResourceValue:&volume forKey:NSURLIsVolumeKey error:nil];
            if (!image) image = [NSImage imageWithSystemSymbolName:systemSymbol(url, kind, volume.boolValue) accessibilityDescription:nil];
            row.insert("icon", symbolData(image ?: [NSImage imageWithSystemSymbolName:@"folder" accessibilityDescription:nil]));
            rows.emplace_back(locationGroup(kind, itemProperty(item, @"com.apple.LSSharedFileList.VolumeGroup")), std::move(row));
        }
        if (strcmp(source.second, "locations") == 0)
            std::stable_sort(rows.begin(), rows.end(), [](const auto &left, const auto &right) { return left.first < right.first; });
        for (const auto &row : rows) entries.append(row.second);
    }
    return QJsonObject{{"entries", entries}};
}
