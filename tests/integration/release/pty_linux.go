//go:build release && !race && linux

package release

import (
	"fmt"
	"os"

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
	if err = unix.IoctlSetInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		return nil, nil, err
	}
	n, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		return nil, nil, err
	}
	slave, err = os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|unix.O_NOCTTY, 0)
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
