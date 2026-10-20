package guestlayout

func exclusiveRenameParameters() (uintptr, uintptr, uintptr) {
	// XNU's exclusive rename rejects an existing destination. Constants follow:
	// https://github.com/apple-oss-distributions/xnu/blob/f6217f891ac0bb64f3d375211650a4c1ff8ca1ea/bsd/kern/syscalls.master#L751
	// https://github.com/apple-oss-distributions/xnu/blob/f6217f891ac0bb64f3d375211650a4c1ff8ca1ea/bsd/sys/fcntl.h#L203
	// https://github.com/apple-oss-distributions/xnu/blob/f6217f891ac0bb64f3d375211650a4c1ff8ca1ea/bsd/sys/stdio.h#L37
	return 488, ^uintptr(1), 4
}
