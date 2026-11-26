//go:build darwin

// Say's icon in the menu bar (the tray): its menu built from what Go gives
// (JSON), a pick handed back to Go (sayTrayPick); with it on, no Dock icon.

#import <Cocoa/Cocoa.h>
#include "_cgo_export.h"

@interface SayTrayTarget : NSObject
@end

@implementation SayTrayTarget
- (void)pick:(NSMenuItem *)item {
	sayTrayPick((int)item.tag);
}
@end

static NSStatusItem *sayTrayItem;
static SayTrayTarget *sayTrayTarget;
static NSImage *sayTrayImage; // Say's bubble (gui/icon/tray.png), a template

// sayTraySetIcon takes the icon's PNG (36 px: 18 points at 2x).
void sayTraySetIcon(const void *png, int n) {
	NSData *data = [NSData dataWithBytes:png length:n];
	dispatch_async(dispatch_get_main_queue(), ^{
		NSImage *image = [[NSImage alloc] initWithData:data];
		if (image != nil) {
			image.size = NSMakeSize(18, 18);
			image.template = YES;
			[sayTrayImage release];
			sayTrayImage = image;
		}
	});
}

static NSMenu *sayTrayMenu(NSArray *items) {
	NSMenu *menu = [[[NSMenu alloc] init] autorelease];
	menu.autoenablesItems = NO;
	for (NSDictionary *d in items) {
		NSString *title = d[@"t"];
		if (title.length == 0) {
			[menu addItem:[NSMenuItem separatorItem]];
			continue;
		}
		NSMenuItem *item = [[[NSMenuItem alloc] initWithTitle:title action:@selector(pick:) keyEquivalent:@""] autorelease];
		item.target = sayTrayTarget;
		item.tag = [d[@"id"] intValue];
		item.enabled = [d[@"on"] boolValue];
		int dot = [d[@"dot"] intValue];
		if (dot != 0) { // a coloured ● before it: green there, red not
			NSColor *colour = dot == 1 ? [NSColor systemGreenColor] : [NSColor systemRedColor];
			NSMutableAttributedString *s = [[[NSMutableAttributedString alloc]
				initWithString:[@"● " stringByAppendingString:title]
				attributes:@{NSFontAttributeName: [NSFont menuFontOfSize:0]}] autorelease];
			[s addAttribute:NSForegroundColorAttributeName value:colour range:NSMakeRange(0, 1)];
			item.attributedTitle = s;
		}
		NSArray *sub = d[@"sub"];
		if ([sub isKindOfClass:[NSArray class]] && sub.count > 0) {
			item.action = nil;
			item.submenu = sayTrayMenu(sub);
		}
		[menu addItem:item];
	}
	return menu;
}

void sayTraySet(int on, const char *json) {
	NSString *js = json ? [[NSString alloc] initWithUTF8String:json] : nil;
	dispatch_async(dispatch_get_main_queue(), ^{
		if (sayTrayTarget == nil) {
			sayTrayTarget = [[SayTrayTarget alloc] init];
		}
		if (!on) {
			if (sayTrayItem != nil) {
				[[NSStatusBar systemStatusBar] removeStatusItem:sayTrayItem];
				[sayTrayItem release];
				sayTrayItem = nil;
			}
			[NSApp setActivationPolicy:NSApplicationActivationPolicyRegular];
			[js release];
			return;
		}
		[NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];
		if (sayTrayItem == nil) {
			sayTrayItem = [[[NSStatusBar systemStatusBar] statusItemWithLength:NSVariableStatusItemLength] retain];
			if (sayTrayImage != nil) {
				sayTrayItem.button.image = sayTrayImage;
			} else {
				sayTrayItem.button.title = @"Say";
			}
		}
		NSArray *items = js ? [NSJSONSerialization JSONObjectWithData:[js dataUsingEncoding:NSUTF8StringEncoding] options:0 error:nil] : nil;
		if ([items isKindOfClass:[NSArray class]]) {
			sayTrayItem.menu = sayTrayMenu(items);
		}
		[js release];
	});
}

// Quitting, told from closing the window: the window's close button hides
// it, while Quit (the Dock's, the app menu's, Cmd+Q) quits. All of them come
// to the window as a close (the window system's terminate: is turned into
// one), so a Quit marks itself first: the Dock's sends the quit Apple
// event, the app menu's item calls terminate: — both are taken here.

static volatile int sayQuitAsked;

@interface SayQuitter : NSObject
@end

@implementation SayQuitter
- (void)quit:(id)sender {
	sayQuitAsked = 1;
	[NSApp terminate:sender];
}
- (void)quitEvent:(NSAppleEventDescriptor *)event withReplyEvent:(NSAppleEventDescriptor *)reply {
	sayQuitAsked = 1;
	[NSApp terminate:nil];
}
@end

static SayQuitter *sayQuitter;

int sayQuitting(void) { return sayQuitAsked; }

void sayCatchQuit(void) {
	dispatch_async(dispatch_get_main_queue(), ^{
		if (sayQuitter == nil) {
			sayQuitter = [[SayQuitter alloc] init];
		}
		[[NSAppleEventManager sharedAppleEventManager] setEventHandler:sayQuitter
			andSelector:@selector(quitEvent:withReplyEvent:)
			forEventClass:kCoreEventClass andEventID:kAEQuitApplication];
		for (NSMenuItem *top in [[NSApp mainMenu] itemArray]) {
			for (NSMenuItem *item in [[top submenu] itemArray]) {
				if (item.action == @selector(terminate:)) {
					item.target = sayQuitter;
					item.action = @selector(quit:);
				}
			}
		}
	});
}
