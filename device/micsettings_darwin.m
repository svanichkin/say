#import <AVFoundation/AVFoundation.h>

// sayShowMicModes opens the system's Mic Mode picker; 0 where the system has
// none (before macOS 12).
int sayShowMicModes(void) {
	if (@available(macOS 12.0, *)) {
		[AVCaptureDevice showSystemUserInterface:AVCaptureSystemUserInterfaceMicrophoneModes];
		return 1;
	}
	return 0;
}
