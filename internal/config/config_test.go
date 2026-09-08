package config

import (
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
	home, _ := os.UserHomeDir()
	if c.DataDirectory != filepath.Join(home, "x") {
		t.Fatalf("tilde not resolved: %s", c.DataDirectory)
	}
	t.Setenv("AGENTBUS_DATA_DIR", "/tmp/override")
	c, _, _ = Load(p)
	if c.DataDirectory != "/tmp/override" {
		t.Fatalf("env override ignored: %s", c.DataDirectory)
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
