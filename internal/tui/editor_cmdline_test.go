package tui

import "testing"

// The cmd.exe command line is plain string building, so it is tested on every
// OS even though only Windows runs it.
func TestCmdExeCommandLine(t *testing.T) {
	for _, tc := range []struct{ ed, path, want string }{
		{`"C:\Program Files\Microsoft VS Code\Code.exe" --wait`, `C:\Users\pat h\config.json`,
			`cmd.exe /S /C ""C:\Program Files\Microsoft VS Code\Code.exe" --wait "C:\Users\pat h\config.json""`},
		{`notepad`, `C:\c.json`, `cmd.exe /S /C "notepad "C:\c.json""`},
		{`code -w`, `x.md`, `cmd.exe /S /C "code -w "x.md""`},
	} {
		if got := cmdExeCommandLine(tc.ed, tc.path); got != tc.want {
			t.Errorf("cmdExeCommandLine(%q, %q) = %q, want %q", tc.ed, tc.path, got, tc.want)
		}
	}
}
