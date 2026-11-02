package main

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

// Linux pty ioctl numbers (asm-generic, shared by amd64 and arm64); the test
// declares them itself because they do not exist in package syscall on darwin.
const (
	linuxTIOCGPTN   = 0x80045430
	linuxTIOCSPTLCK = 0x40045431
)

func ptyIoctl(t *testing.T, fd uintptr, req uintptr, arg *int32) {
	t.Helper()
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(unsafe.Pointer(arg))); errno != 0 {
		t.Fatalf("ioctl %#x: %v", req, errno)
	}
}

// openPTYSlave returns the slave end of a fresh pseudo-terminal.
func openPTYSlave(t *testing.T) *os.File {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("the pty positive control is implemented for linux only")
	}
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("no /dev/ptmx: %v", err)
	}
	t.Cleanup(func() { _ = master.Close() })
	var unlock, n int32
	ptyIoctl(t, master.Fd(), linuxTIOCSPTLCK, &unlock)
	ptyIoctl(t, master.Fd(), linuxTIOCGPTN, &n)
	slave, err := os.OpenFile("/dev/pts/"+strconv.Itoa(int(n)), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = slave.Close() })
	return slave
}

func TestStdinIsTerminalRejectsNullDeviceAndPipeAndAcceptsAPty(t *testing.T) {
	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = null.Close() }()
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pr.Close() }()
	defer func() { _ = pw.Close() }()
	if fdIsTerminal(null.Fd()) {
		t.Error("/dev/null is a character device but not a terminal")
	}
	if fdIsTerminal(pr.Fd()) {
		t.Error("a pipe is not a terminal")
	}
	// Positive control: without it the two rejections could pass for a check
	// that always answers false.
	if !fdIsTerminal(openPTYSlave(t).Fd()) {
		t.Error("a pty slave is a terminal")
	}
}

func TestConfirmApplyAcceptsOnlyYOrUpperY(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		want        bool
	}{
		{"lower-y", "y\n", true},
		{"upper-y", "Y\n", true},
		{"yes", "yes\n", false},
		{"n", "n\n", false},
		{"upper-n", "N\n", false},
		{"empty-line", "\n", false},
		{"leading-space", " y\n", false},
		{"trailing-space", "y \n", false},
		{"eof", "", false},
		{"y-then-eof-without-newline", "y", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var prompt strings.Builder
			if got := confirmApply(strings.NewReader(tc.input), &prompt); got != tc.want {
				t.Errorf("confirmApply(%q) = %v, want %v", tc.input, got, tc.want)
			}
			if prompt.String() != "Apply this plan? [y/N] " {
				t.Errorf("prompt %q", prompt.String())
			}
		})
	}
}
