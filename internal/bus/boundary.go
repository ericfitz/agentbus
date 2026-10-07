package bus

import (
	"fmt"
	"path"
	"strings"
)

// BoundaryError says the data directory sits across the Windows-WSL
// boundary. SQLite's POSIX/Win32 locks and the WAL shared-memory index
// (agentbus.db-shm) do not work between Windows processes and WSL processes
// on one file, so concurrent use can corrupt the database (#35).
type BoundaryError struct {
	Dir        string
	Filesystem string
	Suggestion string
}

func (e *BoundaryError) Error() string {
	return fmt.Sprintf("data directory %s is on %s, across the Windows-WSL boundary: the database must stay on one side (SQLite locking does not work between Windows and WSL processes on one file); use a directory on this side's own disk, for example %s", e.Dir, e.Filesystem, e.Suggestion)
}

// checkBoundary refuses dir when platformBoundary (per OS) says it is
// across the boundary. Outside WSL, and on other filesystems, it is a no-op.
func checkBoundary(dir string) error {
	fs, suggestion, refused := platformBoundary(dir)
	if !refused {
		return nil
	}
	return &BoundaryError{Dir: dir, Filesystem: fs, Suggestion: suggestion}
}

// mountFSType returns the filesystem type of the mount containing dir
// according to mountinfo (the content of /proc/self/mountinfo): the longest
// mount point that equals dir or is a path prefix of it. dir must be
// absolute and already symlink-resolved; ok is false when nothing matches.
func mountFSType(mountinfo, dir string) (fstype string, ok bool) {
	dir = path.Clean(dir)
	if !path.IsAbs(dir) {
		return "", false
	}
	best := -1
	for _, line := range strings.Split(mountinfo, "\n") {
		// Fields: id parent major:minor root mountpoint options [optional
		// fields...] - fstype source superoptions. The "-" separator ends
		// the variable-length optional fields.
		f := strings.Fields(line)
		sep := -1
		for i, w := range f {
			if w == "-" {
				sep = i
				break
			}
		}
		if sep < 5 || sep+1 >= len(f) {
			continue
		}
		mp := path.Clean(unescapeMount(f[4]))
		if mp != dir && !strings.HasPrefix(dir, strings.TrimSuffix(mp, "/")+"/") {
			continue
		}
		if len(mp) > best {
			best, fstype, ok = len(mp), f[sep+1], true
		}
	}
	return fstype, ok
}

// unescapeMount decodes mountinfo's octal escapes for space, tab, newline
// and backslash.
func unescapeMount(s string) string {
	r := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	return r.Replace(s)
}

// wslPathRefusal reports whether abs, a Windows path, is inside a WSL
// distribution: under \\wsl$\ or \\wsl.localhost\ directly, or through a
// mapped drive letter that driveTarget (nil: no mapping lookup) resolves
// to such a UNC path. fs names what was found, for the error.
func wslPathRefusal(abs string, driveTarget func(drive string) (string, bool)) (fs string, refused bool) {
	if isWSLUNC(abs) {
		return "a WSL distribution's filesystem (" + abs + ")", true
	}
	if len(abs) >= 2 && abs[1] == ':' && driveTarget != nil {
		drive := strings.ToUpper(abs[:1]) + ":"
		if target, ok := driveTarget(drive); ok && isWSLUNC(target) {
			return "drive " + drive + ", mapped to " + target + " (a WSL distribution's filesystem)", true
		}
	}
	return "", false
}

func isWSLUNC(p string) bool {
	p = strings.ToLower(strings.ReplaceAll(p, `\`, "/"))
	return strings.HasPrefix(p, "//wsl$/") || strings.HasPrefix(p, "//wsl.localhost/")
}
