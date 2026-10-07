package bus

import (
	"errors"
	"strings"
	"testing"
)

// Recorded from a WSL2 Ubuntu session (abridged): / ext4, /tmp tmpfs,
// /mnt/c 9p (Windows C: drive), /mnt/d drvfs, and an ext4 mount nested
// under /mnt/c. A mount point with a space is escaped as \040.
const recordedMountinfo = `35 25 0:33 / / rw,relatime - ext4 /dev/sdc rw,discard,errors=remount-ro,data=ordered
60 35 0:41 / /tmp rw,nosuid,nodev - tmpfs tmpfs rw
78 35 0:50 / /mnt/c rw,noatime - 9p drvfs rw,dirsync,aname=drvfs;path=C:\;uid=1000;gid=1000;symlinkroot=/mnt/
79 35 0:51 / /mnt/d rw,noatime - drvfs D: rw,dirsync
80 78 0:33 /nested /mnt/c/nested rw,relatime - ext4 /dev/sdc rw
81 35 0:52 / /mnt/with\040space rw - 9p drvfs rw
`

func TestMountFSType(t *testing.T) {
	cases := []struct {
		dir, want string
		ok        bool
	}{
		{"/home/pat/.local/share/agentbus", "ext4", true},
		{"/tmp/agentbus", "tmpfs", true},
		{"/mnt/c/Users/pat/agentbus", "9p", true},
		{"/mnt/c", "9p", true},
		{"/mnt/cx/agentbus", "ext4", true}, // /mnt/cx is not under /mnt/c
		{"/mnt/d/agentbus", "drvfs", true},
		{"/mnt/c/nested/agentbus", "ext4", true}, // nested Linux mount under a 9p mount point
		{"/mnt/with space/x", "9p", true},
		{"relative/dir", "", false},
	}
	for _, c := range cases {
		got, ok := mountFSType(recordedMountinfo, c.dir)
		if got != c.want || ok != c.ok {
			t.Errorf("mountFSType(%q) = %q, %v; want %q, %v", c.dir, got, ok, c.want, c.ok)
		}
	}
	if _, ok := mountFSType("garbage line\n\n- -\n", "/x"); ok {
		t.Fatal("malformed mountinfo must not match")
	}
}

func TestWSLPathRefusal(t *testing.T) {
	drives := func(d string) (string, bool) {
		switch d {
		case "Z:":
			return `\\wsl.localhost\Ubuntu\home\pat`, true
		case "Y:":
			return `\\fileserver\share`, true
		}
		return "", false
	}
	cases := []struct {
		abs     string
		refused bool
	}{
		{`\\wsl$\Ubuntu\home\pat\agentbus`, true},
		{`\\WSL$\Ubuntu\x`, true},
		{`\\wsl.localhost\Ubuntu\home\pat`, true},
		{`Z:\agentbus`, true},
		{`z:\agentbus`, true},
		{`Y:\agentbus`, false},
		{`C:\Users\pat\AppData\Local\agentbus`, false},
		{`\\server\wsl$\x`, false},
		{`\\wslx\Ubuntu`, false},
	}
	for _, c := range cases {
		fs, refused := wslPathRefusal(c.abs, drives)
		if refused != c.refused {
			t.Errorf("wslPathRefusal(%q) = %q, %v; want refused=%v", c.abs, fs, refused, c.refused)
		}
	}
	if _, refused := wslPathRefusal(`Z:\x`, nil); refused {
		t.Fatal("no drive resolver: a drive letter path is allowed")
	}
}

func TestBoundaryErrorText(t *testing.T) {
	err := error(&BoundaryError{Dir: "/mnt/c/x", Filesystem: "9p", Suggestion: "$HOME/.local/share/agentbus"})
	var be *BoundaryError
	if !errors.As(err, &be) {
		t.Fatal("BoundaryError must be matchable with errors.As")
	}
	for _, want := range []string{"/mnt/c/x", "9p", "one side", "$HOME/.local/share/agentbus"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q lacks %q", err, want)
		}
	}
}
