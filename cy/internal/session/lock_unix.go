//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package session

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

var errWouldBlock = errors.New("lock would block")

func acquireLock(file *os.File) error {
	err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
		return errWouldBlock
	}
	return err
}

func releaseLock(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_UN)
}
