//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package session

import (
	"errors"
	"os"
)

var errWouldBlock = errors.New("lock would block")

func acquireLock(*os.File) error {
	return errors.New("exclusive session locks are unsupported on this platform")
}

func releaseLock(*os.File) error { return nil }
