//go:build darwin

package device

/*
#cgo LDFLAGS: -framework IOKit -framework CoreFoundation
#include <IOKit/ps/IOPowerSources.h>
#include <IOKit/ps/IOPSKeys.h>

// sayHasBattery: an internal battery among the power sources (a MacBook).
static int sayHasBattery(void) {
	CFTypeRef info = IOPSCopyPowerSourcesInfo();
	if (info == NULL) return 0;
	CFArrayRef list = IOPSCopyPowerSourcesList(info);
	int found = 0;
	if (list != NULL) {
		for (CFIndex i = 0; i < CFArrayGetCount(list) && !found; i++) {
			CFDictionaryRef d = IOPSGetPowerSourceDescription(info, CFArrayGetValueAtIndex(list, i));
			if (d == NULL) continue;
			CFStringRef type = CFDictionaryGetValue(d, CFSTR(kIOPSTypeKey));
			if (type != NULL && CFStringCompare(type, CFSTR(kIOPSInternalBatteryType), 0) == kCFCompareEqualTo) found = 1;
		}
		CFRelease(list);
	}
	CFRelease(info);
	return found;
}
*/
import "C"

func hasBattery() bool { return C.sayHasBattery() == 1 }
