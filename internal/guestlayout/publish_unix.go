//go:build darwin || (linux && (amd64 || arm64))

package guestlayout

import (
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

func publishDirectory(source, destination string) error {
	from, err := syscall.BytePtrFromString(source)
	if err != nil {
		return err
	}
	to, err := syscall.BytePtrFromString(destination)
	if err != nil {
		return err
	}
	trap, directory, flags := exclusiveRenameParameters()
	// Exclusive rename protects the destination even if another process creates
	// it after the initial existence check; ordinary os.Rename can replace it.
	_, _, errno := syscall.Syscall6(trap, directory, uintptr(unsafe.Pointer(from)), directory, uintptr(unsafe.Pointer(to)), flags, 0)
	runtime.KeepAlive(from)
	runtime.KeepAlive(to)
	if errno != 0 {
		return &os.LinkError{Op: "rename-exclusive", Old: source, New: destination, Err: errno}
	}
	return nil
}
