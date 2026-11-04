package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

var updateGolden = flag.Bool("update-golden", false, "rewrite init golden files")

// TestInitGlobalGolden records the exact output, CLI calls and written files
// of `init --global` so the harness-table refactor can prove it changed
// nothing. Regenerate with -update-golden only for an intended change.
func TestInitGlobalGolden(t *testing.T) {
	scenarios := []struct {
		name    string
		homes   []string
		harness string
	}{
		{"all", []string{".claude", ".codex", ".grok"}, ""},
		{"claude", []string{".claude"}, "claude"},
		{"codex", []string{".codex"}, "codex"},
		{"grok", []string{".grok"}, "grok"},
	}
	for _, sc := range scenarios {
		for _, mode := range []string{"run", "dryrun"} {
			t.Run(sc.name+"-"+mode, func(t *testing.T) {
				home := t.TempDir()
				f := &fakeHarness{home: home}
				for _, d := range sc.homes {
					if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
						t.Fatal(err)
					}
				}
				o := initOpts(t, home, f)
				o.Global = true
				o.Harness = sc.harness
				o.DryRun = mode == "dryrun"
				var out bytes.Buffer
				if err := Init(o, &out); err != nil {
					t.Fatal(err, out.String())
				}

				var b strings.Builder
				b.WriteString(strings.ReplaceAll(filepath.ToSlash(out.String()), filepath.ToSlash(home), "~"))
				if !strings.HasSuffix(b.String(), "\n") {
					b.WriteString("\n")
				}
				b.WriteString("--- calls\n")
				for _, c := range f.calls {
					b.WriteString(c + "\n")
				}
				b.WriteString("--- files\n")
				var files []string
				err := filepath.WalkDir(home, func(p string, d fs.DirEntry, err error) error {
					if err != nil || d.IsDir() {
						return err
					}
					body, err := os.ReadFile(p)
					if err != nil {
						return err
					}
					rel, err := filepath.Rel(home, p)
					if err != nil {
						return err
					}
					sum := sha256.Sum256(body)
					files = append(files, filepath.ToSlash(rel)+" "+hex.EncodeToString(sum[:])[:12])
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
				sort.Strings(files)
				for _, l := range files {
					b.WriteString(l + "\n")
				}

				golden := filepath.Join("testdata", "init_golden", sc.name+"-"+mode+".txt")
				if *updateGolden {
					if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(golden, []byte(b.String()), 0o644); err != nil {
						t.Fatal(err)
					}
					return
				}
				want, err := os.ReadFile(golden)
				if err != nil {
					t.Fatal(err)
				}
				if string(want) != b.String() {
					t.Fatalf("golden mismatch for %s\n--- want\n%s\n--- got\n%s", golden, want, b.String())
				}
			})
		}
	}
}
