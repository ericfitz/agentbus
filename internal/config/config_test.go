package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, dir, body string) string {
	t.Helper()
	p := filepath.Join(dir, "config.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDefaultsWhenNoFile(t *testing.T) {
	t.Setenv("AGENTBUS_CONFIG", filepath.Join(t.TempDir(), "missing.json"))
	t.Setenv("AGENTBUS_DATA_DIR", "")
	c, _, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if c.SQLiteBudgetMiB != 2048 || c.ReceiveMaxWaitSeconds != 60 || c.LogLevel != "info" {
		t.Fatalf("defaults wrong: %+v", c)
	}
}

func TestRejectsUnknownKey(t *testing.T) {
	p := write(t, t.TempDir(), `{"redis_budget_mib": 5}`)
	_, _, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "redis_budget_mib") {
		t.Fatalf("want unknown-key error naming the key, got %v", err)
	}
}

func TestThemesFromFileAndOldColorKeysRejected(t *testing.T) {
	p := write(t, t.TempDir(), `{"theme": "night", "themes": [{"name": "night", "agent": "BrightBlue"}]}`)
	c, _, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Theme != "night" || len(c.Themes) != 2 || c.Themes[0].Agent != "BrightBlue" || c.Themes[0].Dim != "" {
		t.Fatalf("themes wrong: %+v", c.Themes)
	}
	if th, ok := c.FindTheme("night"); !ok || th.Agent != "BrightBlue" {
		t.Fatalf("FindTheme: %+v %v", th, ok)
	}
	if th, ok := c.FindTheme("default"); !ok || th != DefaultTheme() {
		t.Fatal("the built-in default theme stays available when the file's list lacks one")
	}
	c, _, err = Load(write(t, t.TempDir(), `{"themes": [{"name": "default", "agent": "white"}]}`))
	if err != nil || len(c.Themes) != 1 || c.Themes[0].Agent != "white" {
		t.Fatalf("a file may redefine default: %v %+v", err, c.Themes)
	}
	if d := Default(); d.Theme != "default" || len(d.Themes) != 1 || d.Themes[0] != DefaultTheme() || DefaultTheme().Agent != "cyan" {
		t.Fatalf("default theme wrong: %+v", d.Themes)
	}
	// A bad color is not a load error; the TUI substitutes the default.
	if _, _, err := Load(write(t, t.TempDir(), `{"themes": [{"name": "x", "warn": "#ff0"}]}`)); err != nil {
		t.Fatalf("theme colors must not be validated at load: %v", err)
	}
	if _, _, err := Load(write(t, t.TempDir(), `{"tui_warn_color": "red"}`)); err == nil || !strings.Contains(err.Error(), "tui_warn_color") {
		t.Fatalf("the old per-color keys are unknown fields now, got %v", err)
	}
}

func TestRejectsOutOfRange(t *testing.T) {
	p := write(t, t.TempDir(), `{"cleanup_free_percent": 95}`)
	_, _, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "cleanup_free_percent") || !strings.Contains(err.Error(), "90") {
		t.Fatalf("want range error naming key and bound, got %v", err)
	}
}

func TestRejectsTrailingObjectAfterConfig(t *testing.T) {
	p := write(t, t.TempDir(), `{"log_level":"debug"}{"log_level":"error"}`)
	_, _, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "exactly one JSON object") {
		t.Fatalf("want trailing-object error, got %v", err)
	}
}

func TestRejectsTrailingJunkAfterConfig(t *testing.T) {
	p := write(t, t.TempDir(), `{"log_level":"debug"} garbage`)
	_, _, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "exactly one JSON object") {
		t.Fatalf("want trailing-junk error, got %v", err)
	}
}

func TestRejectsTopLevelNull(t *testing.T) {
	p := write(t, t.TempDir(), `null`)
	_, _, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "null") {
		t.Fatalf("want top-level null rejected, got %v", err)
	}
}

func TestRejectsNullField(t *testing.T) {
	p := write(t, t.TempDir(), `{"sqlite_budget_mib": null}`)
	_, _, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "sqlite_budget_mib") || !strings.Contains(err.Error(), "null") {
		t.Fatalf("want sqlite_budget_mib null rejected, got %v", err)
	}
}

func TestRejectsEndpointWithoutModel(t *testing.T) {
	p := write(t, t.TempDir(), `{"embedding_endpoint": "http://localhost:11434/v1/embeddings"}`)
	_, _, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "embedding_model") {
		t.Fatalf("want embedding_model required, got %v", err)
	}
}

func TestRejectsNonURLEndpoint(t *testing.T) {
	p := write(t, t.TempDir(), `{"embedding_endpoint": "not-a-url", "embedding_model": "m"}`)
	_, _, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "embedding_endpoint") {
		t.Fatalf("want embedding_endpoint URL error, got %v", err)
	}
}

func TestDataDirEnvOverrideAndTilde(t *testing.T) {
	p := write(t, t.TempDir(), `{"data_directory": "~/x"}`)
	t.Setenv("AGENTBUS_DATA_DIR", "")
	c, _, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if c.DataDirectory != filepath.Join(home, "x") {
		t.Fatalf("tilde not resolved: %s", c.DataDirectory)
	}
	t.Setenv("AGENTBUS_DATA_DIR", "/tmp/override")
	c, _, err = Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.DataDirectory != "/tmp/override" {
		t.Fatalf("env override ignored: %s", c.DataDirectory)
	}
}

func TestRejectsEmptyConfigFile(t *testing.T) {
	p := write(t, t.TempDir(), ``)
	_, _, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("want empty-file error, got %v", err)
	}
}

func TestRejectsWrongType(t *testing.T) {
	p := write(t, t.TempDir(), `{"sqlite_budget_mib": "not-a-number"}`)
	_, _, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "sqlite_budget_mib") {
		t.Fatalf("want type error naming the key, got %v", err)
	}
}

func TestRejectsInvalidLogLevel(t *testing.T) {
	p := write(t, t.TempDir(), `{"log_level": "verbose"}`)
	_, _, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "log_level") {
		t.Fatalf("want log_level error, got %v", err)
	}
}

func TestRejectsReceiveDefaultAboveMax(t *testing.T) {
	p := write(t, t.TempDir(), `{"receive_default_count": 500, "receive_max_count": 100}`)
	_, _, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "receive_default_count") {
		t.Fatalf("want ordering error, got %v", err)
	}
}

func TestRejectsTooManyInspectionCommandEntries(t *testing.T) {
	entries := make([]string, 65)
	for i := range entries {
		entries[i] = "arg"
	}
	enc, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	p := write(t, t.TempDir(), `{"inspection_command": `+string(enc)+`}`)
	_, _, err = Load(p)
	if err == nil || !strings.Contains(err.Error(), "inspection_command") {
		t.Fatalf("want inspection_command entry-count error, got %v", err)
	}
}

func TestRejectsOversizedInspectionCommand(t *testing.T) {
	entries := make([]string, 64)
	for i := range entries {
		entries[i] = strings.Repeat("x", 600) // 64 * ~600B > 32 KiB encoded
	}
	enc, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	p := write(t, t.TempDir(), `{"inspection_command": `+string(enc)+`}`)
	_, _, err = Load(p)
	if err == nil || !strings.Contains(err.Error(), "32 KiB") {
		t.Fatalf("want inspection_command size error, got %v", err)
	}
}

// TestConfigPathParamTakesPrecedenceOverEnv covers the --config precedence
// Load implements: an explicit path always wins over AGENTBUS_CONFIG.
func TestConfigPathParamTakesPrecedenceOverEnv(t *testing.T) {
	dir := t.TempDir()
	envPath := write(t, dir, `{"log_level": "debug"}`)
	argPath := filepath.Join(dir, "arg-config.json")
	if err := os.WriteFile(argPath, []byte(`{"log_level": "warn"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTBUS_CONFIG", envPath)
	c, gotPath, err := Load(argPath)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != argPath || c.LogLevel != "warn" {
		t.Fatalf("explicit path must win over AGENTBUS_CONFIG: path=%s level=%s", gotPath, c.LogLevel)
	}
}

func TestRelativePathResolvesAgainstConfigDir(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, `{"data_directory": "data", "embedding_endpoint": "http://h/v1/embeddings", "embedding_model": "m", "embedding_api_key_file": "key.txt"}`)
	t.Setenv("AGENTBUS_DATA_DIR", "")
	c, _, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.DataDirectory != filepath.Join(dir, "data") || c.EmbeddingAPIKeyFile != filepath.Join(dir, "key.txt") {
		t.Fatalf("relative paths not resolved: %+v", c)
	}
}

func TestRejectsEmbeddingQueryTimeoutOutOfRange(t *testing.T) {
	p := write(t, t.TempDir(), `{"embedding_query_timeout_seconds": 200}`)
	_, _, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "embedding_query_timeout_seconds") || !strings.Contains(err.Error(), "120") {
		t.Fatalf("want range error naming key and bound, got %v", err)
	}
}

func TestTUINameDefaultsToOSUser(t *testing.T) {
	c := Default()
	if c.TUIName == "" {
		t.Fatal("tui_name default must not be empty")
	}
	if strings.ContainsRune(c.TUIName, '/') {
		t.Fatalf("tui_name default %q must not contain '/'", c.TUIName)
	}
}

func TestTUINameFromFile(t *testing.T) {
	p := write(t, t.TempDir(), `{"tui_name": "eric"}`)
	c, _, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.TUIName != "eric" {
		t.Fatalf("want eric, got %q", c.TUIName)
	}
}

func TestRejectsEmptyTUIName(t *testing.T) {
	p := write(t, t.TempDir(), `{"tui_name": ""}`)
	_, _, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "tui_name") {
		t.Fatalf("want tui_name error, got %v", err)
	}
}

func TestDataDirEnvResolvesTildeAndRelative(t *testing.T) {
	p := write(t, t.TempDir(), `{}`)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for env, want := range map[string]string{
		"~/x":   filepath.Join(home, "x"),
		"rel/d": filepath.Join(cwd, "rel/d"),
	} {
		t.Setenv("AGENTBUS_DATA_DIR", env)
		c, _, err := Load(p)
		if err != nil {
			t.Fatal(err)
		}
		if c.DataDirectory != want {
			t.Fatalf("AGENTBUS_DATA_DIR=%s: got %s, want %s", env, c.DataDirectory, want)
		}
	}
}
