//go:build darwin || linux

package detach

import "syscall"

func detachedSysProcAttr() *syscall.SysProcAttr {
	// Keep child runs independent so parent exits do not stop user work.
	return &syscall.SysProcAttr{Setpgid: true}
}
