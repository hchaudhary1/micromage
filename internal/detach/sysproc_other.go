//go:build !darwin && !linux

package detach

import "syscall"

func detachedSysProcAttr() *syscall.SysProcAttr {
	return nil
}
