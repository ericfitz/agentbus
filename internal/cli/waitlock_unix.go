//go:build unix

package cli

import "syscall"

// terminateWait stops the wait process pid the way a replacing wait does on
// Unix: SIGTERM, which the wait's signal handler (installed in main before
// acquireWaitLock) turns into ErrWaitReplaced and exit code 3.
func terminateWait(pid int) error {
	return syscall.Kill(pid, syscall.SIGTERM)
}
