//go:build darwin

package gui

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa
#import <Cocoa/Cocoa.h>

// sayHideApp takes the window off the screen, the app kept running (its
// Dock icon brings it back, as Cmd+H's).
static void sayHideApp(void) {
	dispatch_async(dispatch_get_main_queue(), ^{ [NSApp hide:nil]; });
}

// sayHidden is 1 while the app is hidden (by Say closing its window, by
// Cmd+H, or by anything else), as NSApp's own notifications say.
static volatile int sayHidden;

static void sayWatchHidden(void) {
	dispatch_async(dispatch_get_main_queue(), ^{
		NSNotificationCenter *nc = [NSNotificationCenter defaultCenter];
		[nc addObserverForName:NSApplicationDidHideNotification object:nil queue:nil
			usingBlock:^(NSNotification *n) { sayHidden = 1; }];
		[nc addObserverForName:NSApplicationDidUnhideNotification object:nil queue:nil
			usingBlock:^(NSNotification *n) { sayHidden = 0; }];
		sayHidden = [NSApp isHidden] ? 1 : 0;
	});
}

static int sayIsHidden(void) { return sayHidden; }

// sayShowApp brings the window back, in front.
static void sayShowApp(void) {
	dispatch_async(dispatch_get_main_queue(), ^{
		[NSApp unhide:nil];
		[NSApp activateIgnoringOtherApps:YES];
	});
}
*/
import "C"

import "sync"

var watchHidden sync.Once

// appHidden reports whether the app is hidden (the window off the screen).
func appHidden() bool {
	watchHidden.Do(func() { C.sayWatchHidden() })
	return C.sayIsHidden() != 0
}

func hideApp() { C.sayHideApp() }

func showApp() { C.sayShowApp() }
