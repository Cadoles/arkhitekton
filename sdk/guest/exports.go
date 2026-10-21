//go:build wasip1

package guest

import "unsafe"

// The buffers the dashboard writes into and reads from. Package variables, so that the collector
// keeps them while the host holds their addresses.
var inBuffer, outBuffer []byte

//go:wasmexport arkhitekton_abi
func abi() int32 { return ABI }

//go:wasmexport arkhitekton_alloc
func alloc(size int32) int32 {
	inBuffer = make([]byte, size)
	if size == 0 {
		return 0
	}
	return int32(uintptr(unsafe.Pointer(&inBuffer[0])))
}

//go:wasmexport arkhitekton_interpret
func interpret(pointer, length int32) int64 {
	_ = pointer
	outBuffer = Handle(inBuffer[:length], registered)
	if len(outBuffer) == 0 {
		return 0
	}
	return int64(uintptr(unsafe.Pointer(&outBuffer[0])))<<32 | int64(len(outBuffer))
}
