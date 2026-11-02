package main

import (
	"syscall"
	"unsafe"
)

// stdinIsTerminal reports whether standard input is a terminal. A mode check
// would treat /dev/null as one (it is a character device), so this asks the
// tty driver through the TIOCGETA ioctl, which only a terminal answers.
func stdinIsTerminal() bool { return fdIsTerminal(0) }

func fdIsTerminal(fd uintptr) bool {
	var t syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TIOCGETA, uintptr(unsafe.Pointer(&t)))
	return errno == 0
}
