//go:build darwin

package procs

import (
	"errors"
	"strings"

	"golang.org/x/sys/unix"
)

type systemTable struct{}

func kinfo(pid int) (*unix.KinfoProc, error) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		// EIO: the kernel returned an empty reply for a pid that does not exist.
		if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ESRCH) || errors.Is(err, unix.EIO) {
			return nil, ErrGone
		}
		return nil, err
	}
	if kp.Proc.P_pid == 0 && pid != 0 {
		return nil, ErrGone // empty reply: no such pid
	}
	return kp, nil
}

func (systemTable) Parent(pid int) (int, error) {
	kp, err := kinfo(pid)
	if err != nil {
		return 0, err
	}
	return int(kp.Eproc.Ppid), nil
}

func (systemTable) Comm(pid int) (string, error) {
	kp, err := kinfo(pid)
	if err != nil {
		return "", err
	}
	b := make([]byte, 0, len(kp.Proc.P_comm))
	for _, c := range kp.Proc.P_comm {
		if c == 0 {
			break
		}
		b = append(b, byte(c))
	}
	return strings.TrimSpace(string(b)), nil
}

func (systemTable) StartTime(pid int) (int64, error) {
	kp, err := kinfo(pid)
	if err != nil {
		return 0, err
	}
	return int64(kp.Proc.P_starttime.Sec)*1_000_000 + int64(kp.Proc.P_starttime.Usec), nil
}
