//go:build linux

package bus

import (
	"os"
	"testing"
)

func TestIsWSL(t *testing.T) {
	fake := func(files map[string]string) func(string) ([]byte, error) {
		return func(p string) ([]byte, error) {
			if v, ok := files[p]; ok {
				return []byte(v), nil
			}
			return nil, os.ErrNotExist
		}
	}
	if !isWSL(fake(map[string]string{"/proc/sys/fs/binfmt_misc/WSLInterop": "enabled\n"})) {
		t.Fatal("WSLInterop present must mean WSL")
	}
	if !isWSL(fake(map[string]string{"/proc/sys/fs/binfmt_misc/WSLInterop-late": "enabled\n"})) {
		t.Fatal("WSLInterop-late present must mean WSL")
	}
	if !isWSL(fake(map[string]string{"/proc/sys/kernel/osrelease": "5.15.167.4-microsoft-standard-WSL2\n"})) {
		t.Fatal("microsoft in osrelease must mean WSL")
	}
	if isWSL(fake(map[string]string{"/proc/sys/kernel/osrelease": "6.8.0-45-generic\n"})) {
		t.Fatal("a plain kernel is not WSL")
	}
}
