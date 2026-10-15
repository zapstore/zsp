//go:build release && !race && darwin

package release

import (
	"bytes"
	"errors"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// openPTY returns a master and slave. The caller closes the slave after the
// child has started so the master reads EOF when the child exits.
func openPTY() (master, slave *os.File, err error) {
	master, err = os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		if err != nil {
			master.Close()
		}
	}()
	if err = unix.IoctlSetInt(int(master.Fd()), unix.TIOCPTYGRANT, 0); err != nil {
		return nil, nil, err
	}
	if err = unix.IoctlSetInt(int(master.Fd()), unix.TIOCPTYUNLK, 0); err != nil {
		return nil, nil, err
	}
	name, err := ptyName(int(master.Fd()))
	if err != nil {
		return nil, nil, err
	}
	slave, err = os.OpenFile(name, os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, err
	}
	err = unix.IoctlSetWinsize(int(slave.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 40, Col: 500})
	if err != nil {
		slave.Close()
		return nil, nil, err
	}
	return master, slave, nil
}

// TIOCPTYGNAME copies a 128-byte NUL-terminated path.
func ptyName(fd int) (string, error) {
	var buf [128]byte
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), uintptr(unix.TIOCPTYGNAME), uintptr(unsafe.Pointer(&buf[0])))
	if errno != 0 {
		return "", errno
	}
	n := bytes.IndexByte(buf[:], 0)
	if n <= 0 {
		return "", errors.New("pty name is empty")
	}
	return string(buf[:n]), nil
}
