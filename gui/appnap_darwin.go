//go:build darwin

package gui

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Foundation
#import <Foundation/Foundation.h>

// sayBeginCallActivity tells macOS that this process does latency-critical
// work for the user (a call): no App Nap, no timer coalescing, whether the
// window is in front or not.
static void sayBeginCallActivity(void) {
	static id activity;
	if (activity != nil) {
		return;
	}
	activity = [[[NSProcessInfo processInfo]
		beginActivityWithOptions:(NSActivityUserInitiatedAllowingIdleSystemSleep | NSActivityLatencyCritical)
		reason:@"Say call"] retain];
}
*/
import "C"

// keepAwake stops App Nap. A command-line tool is never napped, but a
// process with a window is once the window is hidden or covered: macOS then
// coalesces its timers and lowers its priority, and the voice, played out
// on a 4 ms tick, comes out in fragments.
func keepAwake() { C.sayBeginCallActivity() }
