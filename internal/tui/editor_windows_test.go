//go:build windows

package tui

import (
	"strings"
	"testing"
)

func TestShellEditorCommandWindows(t *testing.T) {
	cmd := shellEditorCommand(`"C:\Program Files\Microsoft VS Code\Code.exe" --wait`, `C:\Users\pat h\config.json`)
	if cmd.SysProcAttr == nil || cmd.SysProcAttr.CmdLine != `cmd.exe /S /C ""C:\Program Files\Microsoft VS Code\Code.exe" --wait "C:\Users\pat h\config.json""` {
		t.Fatalf("CmdLine = %q", cmd.SysProcAttr.CmdLine)
	}
	if got := strings.ToLower(cmd.Path); !strings.HasSuffix(got, `\system32\cmd.exe`) {
		t.Fatalf("cmd.Path = %q, want the system directory's cmd.exe", cmd.Path)
	}
	if defaultEditor != "notepad" {
		t.Fatalf("default editor = %q", defaultEditor)
	}
}

func TestEditorErrTextExplainsMissingCommandWindows(t *testing.T) {
	t.Setenv("VISUAL", "no-such-editor-xyz")
	err := editorCommand("x.md").Run()
	if got := editorErrText(err); !strings.Contains(got, "not found") || !strings.Contains(got, "no-such-editor-xyz") || !strings.Contains(got, "$VISUAL") {
		t.Fatalf("editorErrText(%v) = %q", err, got)
	}
}
