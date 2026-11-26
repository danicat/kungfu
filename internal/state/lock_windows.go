// Copyright 2026 Daniela Petruzalek
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build windows

package state

import (
	"fmt"
	"os"
	"runtime"
	"syscall"
	"time"
	"unsafe"
)

var (
	modkernel32      = syscall.NewLazyDLL("kernel32.dll")
	procLockFileEx   = modkernel32.NewProc("LockFileEx")
	procUnlockFileEx = modkernel32.NewProc("UnlockFileEx")
)

const (
	lockfileFailImmediately = 0x00000001
	lockfileExclusiveLock   = 0x00000002
	errLockViolation        = syscall.Errno(33)
)

func openLockFile(lockPath string) (*os.File, error) {
	if fi, err := os.Lstat(lockPath); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("refusing to open symlink lock file %s", lockPath)
	}
	return os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
}

func lockFileExclusive(f *os.File, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	deadline := time.Now().Add(timeout)
	backoff := 5 * time.Millisecond

	for {
		var ol syscall.Overlapped
		r1, _, e1 := syscall.SyscallN(
			procLockFileEx.Addr(),
			f.Fd(),
			uintptr(lockfileExclusiveLock|lockfileFailImmediately),
			0,
			1,
			0,
			uintptr(unsafe.Pointer(&ol)),
		)
		runtime.KeepAlive(f)
		if r1 != 0 {
			return nil
		}
		if e1 != errLockViolation && e1 != syscall.ERROR_IO_PENDING {
			return e1
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s waiting for state lock", timeout)
		}
		time.Sleep(backoff)
		if backoff < 100*time.Millisecond {
			backoff *= 2
		}
	}
}

func unlockFile(f *os.File) error {
	var ol syscall.Overlapped
	r1, _, e1 := syscall.SyscallN(
		procUnlockFileEx.Addr(),
		f.Fd(),
		0,
		1,
		0,
		uintptr(unsafe.Pointer(&ol)),
	)
	runtime.KeepAlive(f)
	if r1 == 0 {
		return e1
	}
	return nil
}
