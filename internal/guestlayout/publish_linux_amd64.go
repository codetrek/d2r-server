package guestlayout

func exclusiveRenameParameters() (uintptr, uintptr, uintptr) {
	// Linux's no-replace rename and AT_FDCWD preserve competing output directories:
	// https://github.com/torvalds/linux/blob/f47d223627f80cb7a1eb14c585fe6cb35de5e984/arch/x86/entry/syscalls/syscall_64.tbl#L328
	// https://github.com/torvalds/linux/blob/f47d223627f80cb7a1eb14c585fe6cb35de5e984/include/uapi/linux/fcntl.h#L106
	// https://github.com/torvalds/linux/blob/f47d223627f80cb7a1eb14c585fe6cb35de5e984/include/uapi/linux/fs.h#L59
	return 316, ^uintptr(99), 1
}
