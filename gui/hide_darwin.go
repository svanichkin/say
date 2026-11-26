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

// sayShowApp brings the window back, in front.
static void sayShowApp(void) {
	dispatch_async(dispatch_get_main_queue(), ^{
		[NSApp unhide:nil];
		[NSApp activateIgnoringOtherApps:YES];
	});
}
*/
import "C"

func hideApp() { C.sayHideApp() }

func showApp() { C.sayShowApp() }
