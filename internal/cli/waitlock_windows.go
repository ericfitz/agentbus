//go:build windows

package cli

import "golang.org/x/sys/windows"

// terminateWait ends the wait process pid with exit code 3, the code a
// replaced wait exits with on Unix (ErrWaitReplaced): no signal except
// Ctrl+C reaches a Windows console process, so the replaced wait is
// terminated outright. Its own cleanup is skipped, which is safe: the
// caller overwrites the pidfile, and SQLite recovers from an abrupt exit.
func terminateWait(pid int) error {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(h) }()
	return windows.TerminateProcess(h, 3)
}
