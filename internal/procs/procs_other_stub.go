//go:build !unix && !windows

package procs

// exists cannot be answered on this platform; every pid is assumed live, so
// Alive degrades to "unknown" and nothing is ever signaled by mistake.
func exists(int) bool { return true }
