package guestlayout

func exclusiveRenameParameters() (uintptr, uintptr, uintptr) {
	// The ARM64 syscall number differs; AT_FDCWD and RENAME_NOREPLACE match amd64:
	// https://github.com/golang/go/blob/d90b98e65320778f3b1f99a6951ab20f04d218b3/src/syscall/zsysnum_linux_arm64.go#L269
	return 276, ^uintptr(99), 1
}
