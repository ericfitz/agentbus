// Package filelock takes exclusive, blocking, cross-process locks on open
// files: flock(LOCK_EX) on Unix, LockFileEx(LOCKFILE_EXCLUSIVE_LOCK) on
// Windows. A lock is released when the file is closed or the process exits,
// so callers hold it for a short critical section and close the file.
package filelock
