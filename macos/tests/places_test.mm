#include "../native/places.mm"
#include <QTemporaryDir>
#include <cassert>

int main() {
    @autoreleasepool {
        QTemporaryDir temporary;
        assert(temporary.isValid());
        const QString path = temporary.path() + QStringLiteral("/雪 # folder");
        NSURL *file = [NSURL fileURLWithPath:path.toNSString()];
        assert(placeEntry(@"Display name", file, placeKind(file, nil), "favorites", @{}).value("path").toString() == path);
        NSURL *virtualURL = [NSURL URLWithString:@"nwnode://domain-AirDrop"];
        const auto location = placeEntry(nil, virtualURL, placeKind(virtualURL, nil), "locations", @{});
        assert(location.value("path").toString().isEmpty());
        assert(location.value("url").toString() == QStringLiteral("nwnode://domain-AirDrop"));
        NSURL *trashURL = [NSURL URLWithString:@"x-finder:Trash"];
        const auto trash = placeEntry(@"/", trashURL, placeKind(trashURL, nil), "locations", @{});
        assert(trash.value("path").toString().isEmpty());
        assert(trash.value("label").toString() != QStringLiteral("/"));
        assert(trash.value("kind").toString() == QStringLiteral("trash"));
        NSURL *shared = [NSURL fileURLWithPath:@"/System/Library/CoreServices/Finder.app/Contents/Resources/MyLibraries/SharedDocuments.cannedSearch"];
        assert(placeEntry(nil, shared, placeKind(shared, nil), "top", @{}).value("path").toString().isEmpty());
        NSURL *custom = [file URLByAppendingPathComponent:@"SharedDocuments.cannedSearch"];
        assert(placeEntry(@"Custom", custom, placeKind(custom, nil), "locations", @{}).value("path").toString() == QString::fromNSString(custom.path));
        assert(!hiddenPlace(@NO, nil, file));
        assert(hiddenPlace(@YES, nil, file));
        assert(hiddenPlace(@NO, @"NeverVisible", file));
        assert(hiddenPlace(nil, nil, nil));
        assert(hiddenPlace(nil, nil, [NSURL URLWithString:@"com-apple-sfl://com.apple.LSSharedFileList.IsRemoteDisc"]));
        NSImage *blackIcon = [NSImage imageWithSize:NSMakeSize(16, 16) flipped:NO drawingHandler:^BOOL(NSRect) {
            [NSColor.blackColor setFill];
            NSRectFill(NSMakeRect(4, 4, 8, 8));
            return YES;
        }];
        const auto png = QByteArray::fromBase64(symbolData(blackIcon).section(QLatin1Char(','), 1).toLatin1());
        NSBitmapImageRep *mask = [NSBitmapImageRep imageRepWithData:[NSData dataWithBytes:png.constData() length:png.size()]];
        NSColor *center = [[mask colorAtX:32 y:32] colorUsingColorSpace:NSColorSpace.genericRGBColorSpace];
        assert(center.alphaComponent > 0.99 && center.redComponent > 0.99 && center.greenComponent > 0.99 && center.blueComponent > 0.99);
        assert([mask colorAtX:0 y:0].alphaComponent < 0.01);

        bool rejected = false;
        try { places(QStringLiteral("places.invalid"), {}); }
        catch (const std::runtime_error &) { rejected = true; }
        assert(rejected);
    }
}
