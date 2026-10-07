//go:build windows

package procs

import (
	"errors"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

type systemTable struct{}

// snapshotEntry finds pid in a Toolhelp32 snapshot of the process table.
func snapshotEntry(pid int) (windows.ProcessEntry32, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return windows.ProcessEntry32{}, err
	}
	defer func() { _ = windows.CloseHandle(snap) }()
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		if int(e.ProcessID) == pid {
			return e, nil
		}
	}
	if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return windows.ProcessEntry32{}, ErrGone
	}
	return windows.ProcessEntry32{}, err
}

func (systemTable) Parent(pid int) (int, error) {
	e, err := snapshotEntry(pid)
	if err != nil {
		return 0, err
	}
	return int(e.ParentProcessID), nil
}

// Comm is the executable file name, with its extension ("claude.exe").
func (systemTable) Comm(pid int) (string, error) {
	e, err := snapshotEntry(pid)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(windows.UTF16ToString(e.ExeFile[:])), nil
}

// StartTime is the creation time from GetProcessTimes, in 100 ns units
// since 1601 (opaque: only equality matters). A pid that does not exist
// returns ErrGone, as does a process that has exited but whose handle
// someone still holds (its exit time is set). When OpenProcess fails for any
// other reason (typically ACCESS_DENIED for another user's process) the
// snapshot decides: absent returns ErrGone, present returns 0 (unknown),
// like procs_other.go.
func (systemTable) StartTime(pid int) (int64, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return 0, ErrGone
		}
		if _, serr := snapshotEntry(pid); serr != nil {
			return 0, serr
		}
		return 0, nil
	}
	defer func() { _ = windows.CloseHandle(h) }()
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &creation, &exit, &kernel, &user); err != nil {
		return 0, err
	}
	if exit.LowDateTime != 0 || exit.HighDateTime != 0 {
		return 0, ErrGone
	}
	return int64(creation.HighDateTime)<<32 | int64(creation.LowDateTime), nil
}
