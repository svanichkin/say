package malgo

// #include "malgo.h"
import "C"
import "unsafe"

// Say's addition to malgo: devices whose data callback is C code.

// InitDeviceRaw initializes a device like InitDevice, but its data
// callback is proc, a C function
//
//	void proc(void *user, void *output, const void *input, ma_uint32 frameCount)
//
// called on the audio thread with user. Go code never runs there: a Go
// callback can wait for the Go scheduler, whose threads have ordinary
// priority. onStop, if set, is called (in Go) when the device stops.
func InitDeviceRaw(context Context, deviceConfig DeviceConfig, proc, user unsafe.Pointer, onStop StopProc) (*Device, error) {
	ptr := C.ma_malloc(C.sizeof_ma_device, nil)
	dev := Device{ptr: &ptr}
	if uintptr(*dev.ptr) == 0 {
		return nil, ErrOutOfMemory
	}
	raw := (*C.maRawData)(C.ma_malloc(C.sizeof_maRawData, nil))
	if raw == nil {
		dev.free()
		return nil, ErrOutOfMemory
	}
	raw.proc = C.maRawDataProc(proc)
	raw.pUser = user
	devConfigC, release := deviceConfig.toC()
	defer release()

	rawDevice := dev.cptr()
	C.goSetDeviceConfigRaw(&devConfigC, raw)
	if result := C.ma_device_init(context.cptr(), &devConfigC, rawDevice); result != 0 {
		C.ma_free(unsafe.Pointer(raw), nil)
		dev.free()
		return nil, errorFromResult(result)
	}
	deviceMutex.Lock()
	stopCallbacks[rawDevice] = onStop
	rawUsers[rawDevice] = unsafe.Pointer(raw)
	deviceMutex.Unlock()
	return &dev, nil
}

// rawUsers are the maRawData of raw devices, freed with them.
var rawUsers = make(map[*C.ma_device]unsafe.Pointer)
