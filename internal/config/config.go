// Package config loads and validates the single Agentbus JSON configuration file.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	DataDirectory                string   `json:"data_directory"`
	SQLiteBudgetMiB              int      `json:"sqlite_budget_mib"`
	MessageRetentionHours        int      `json:"message_retention_hours"`
	CleanupFreePercent           int      `json:"cleanup_free_percent"`
	CleanupIntervalSeconds       int      `json:"cleanup_interval_seconds"`
	CursorIdleHours              int      `json:"cursor_idle_hours"`
	TombstoneMinHours            int      `json:"tombstone_min_hours"`
	ReceiptRetentionMinutes      int      `json:"receipt_retention_minutes"`
	MaxMessageKiB                int      `json:"max_message_kib"`
	SendMessagesPerSecond        int      `json:"send_messages_per_second"`
	SendKiBPerSecond             int      `json:"send_kib_per_second"`
	ReceiveDefaultCount          int      `json:"receive_default_count"`
	ReceiveMaxCount              int      `json:"receive_max_count"`
	ReceiveMaxWaitSeconds        int      `json:"receive_max_wait_seconds"`
	ResultDefaultKiB             int      `json:"result_default_kib"`
	DiscoveryEnabled             bool     `json:"discovery_enabled"`
	InspectionCommand            []string `json:"inspection_command"`
	InspectionTimeoutSeconds     float64  `json:"inspection_timeout_seconds"`
	EmbeddingEndpoint            string   `json:"embedding_endpoint"`
	EmbeddingModel               string   `json:"embedding_model"`
	EmbeddingAPIKeyFile          string   `json:"embedding_api_key_file"`
	EmbeddingQueryTimeoutSeconds float64  `json:"embedding_query_timeout_seconds"`
	LogLevel                     string   `json:"log_level"`
	TUIName                      string   `json:"tui_name"`
	Theme                        string   `json:"theme"`  // name of the entry in Themes the TUI applies
	Themes                       []Theme  `json:"themes"` // named color sets; the TUI falls back to DefaultTheme per missing or invalid value

	// Path is the config file that was loaded (or would have been). Not a setting.
	Path string `json:"-"`
}

func Default() Config {
	return Config{
		DataDirectory:                "~/.local/share/agentbus",
		SQLiteBudgetMiB:              2048,
		MessageRetentionHours:        168,
		CleanupFreePercent:           25,
		CleanupIntervalSeconds:       60,
		CursorIdleHours:              72,
		TombstoneMinHours:            72,
		ReceiptRetentionMinutes:      60,
		MaxMessageKiB:                64,
		SendMessagesPerSecond:        100,
		SendKiBPerSecond:             1024,
		ReceiveDefaultCount:          100,
		ReceiveMaxCount:              1000,
		ReceiveMaxWaitSeconds:        60,
		ResultDefaultKiB:             64,
		DiscoveryEnabled:             true,
		InspectionCommand:            []string{},
		InspectionTimeoutSeconds:     5,
		EmbeddingQueryTimeoutSeconds: 10,
		LogLevel:                     "info",
		TUIName:                      defaultTUIName(),
		Theme:                        "default",
		Themes:                       []Theme{DefaultTheme()},
	}
}

// Theme is one named set of TUI colors. Values are not validated at load
// time: the TUI substitutes DefaultTheme's value for any that is missing or
// not a color (see ColorIndex).
type Theme struct {
	Name       string `json:"name"`
	Background string `json:"background"`
	Text       string `json:"text"`
	Dim        string `json:"dim"`
	Agent      string `json:"agent"`
	User       string `json:"user"`
	Memory     string `json:"memory"`
	Health     string `json:"health"`
	Warn       string `json:"warn"`
	Error      string `json:"error"`
	Selection  string `json:"selection"`
}

// DefaultTheme is the built-in "default" theme and the per-value fallback.
func DefaultTheme() Theme {
	return Theme{Name: "default", Background: "default", Text: "default", Dim: "brightblack", Agent: "cyan", User: "yellow", Memory: "magenta", Health: "green", Warn: "yellow", Error: "red", Selection: "brightblack"}
}

// Colors lists the theme's values as (key, value) pairs in a fixed order,
// keyed by the JSON field names.
func (t Theme) Colors() [][2]string {
	return [][2]string{
		{"background", t.Background},
		{"text", t.Text},
		{"dim", t.Dim},
		{"agent", t.Agent},
		{"user", t.User},
		{"memory", t.Memory},
		{"health", t.Health},
		{"warn", t.Warn},
		{"error", t.Error},
		{"selection", t.Selection},
	}
}

// FindTheme returns the entry of Themes named name, or false.
func (c Config) FindTheme(name string) (Theme, bool) {
	for _, t := range c.Themes {
		if t.Name == name {
			return t, true
		}
	}
	return Theme{}, false
}

var ansiNames = []string{"black", "red", "green", "yellow", "blue", "magenta", "cyan", "white"}

// ColorForms is the accepted-values sentence for the TUI's theme warning.
const ColorForms = "black, red, green, yellow, blue, magenta, cyan, white, their bright forms such as brightblack, 0-15, or default"

// ColorIndex parses a color setting: the sixteen ANSI names (bright forms
// prefixed with "bright"), the integers 0-15, or "default" (-1);
// case-insensitive, surrounding whitespace ignored. Only ANSI colors are
// accepted on purpose: #RGB is rejected so a later version can add it
// without silently changing behavior.
func ColorIndex(s string) (int, error) {
	v := strings.ToLower(strings.TrimSpace(s))
	if v == "default" {
		return -1, nil
	}
	if n, err := strconv.Atoi(v); err == nil {
		if n < 0 || n > 15 {
			return 0, errors.New("index out of range")
		}
		return n, nil
	}
	base, bright := strings.CutPrefix(v, "bright")
	for i, name := range ansiNames {
		if base == name {
			if bright {
				i += 8
			}
			return i, nil
		}
	}
	return 0, errors.New("unknown color")
}

// defaultTUIName is the OS user name, the identity `agentbus tui` registers
// under unless tui_name or --as says otherwise. On Windows user.Current
// returns DOMAIN\name; keep the part after the last backslash. A '/' would
// fail the bus's name rule, so it is replaced.
func defaultTUIName() string {
	u, err := user.Current()
	if err != nil || u.Username == "" {
		return "human"
	}
	name := u.Username
	if i := strings.LastIndex(name, `\`); i >= 0 {
		name = name[i+1:]
	}
	return strings.ReplaceAll(name, "/", "-")
}

// DefaultPath is the config file used when neither --config nor AGENTBUS_CONFIG is set.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".config", "agentbus", "config.json"), nil
}

// Load reads path (or AGENTBUS_CONFIG, or the default path), applies defaults,
// validates strictly, then applies AGENTBUS_DATA_DIR. A missing file means defaults.
func Load(path string) (Config, string, error) {
	c := Default()
	if path == "" {
		path = os.Getenv("AGENTBUS_CONFIG")
	}
	if path == "" {
		var err error
		path, err = DefaultPath()
		if err != nil {
			return c, "", err
		}
	}
	body, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return c, path, fmt.Errorf("read %s: %w", path, err)
	default:
		// Decoding a JSON array into a non-empty slice reuses its elements,
		// so the file's first theme would inherit the built-in default's
		// values; start from an empty list instead and put the built-in
		// "default" back below if the file did not define one.
		c.Themes = nil
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&c); err != nil {
			return c, path, fmt.Errorf("%s: %w", path, err)
		}
		if _, ok := c.FindTheme("default"); !ok {
			c.Themes = append(c.Themes, DefaultTheme())
		}
		// The config file must hold exactly one JSON object: Decoder.Decode
		// only reads one value and, unlike json.Unmarshal, silently ignores
		// anything after it. A second Decode on the same stream must find
		// nothing but EOF, else there is trailing content (another object,
		// junk, ...).
		var trailing json.RawMessage
		if err := dec.Decode(&trailing); err != io.EOF {
			return c, path, fmt.Errorf("%s: must contain exactly one JSON object", path)
		}
		// A top-level `null`, or `null` for a non-nullable setting, decodes
		// into &c above with no error (encoding/json leaves a non-pointer
		// struct field unchanged for a null literal), silently keeping
		// defaults. Re-parse into a raw map to reject both explicitly: a
		// nil map means the top-level value was null (or not an object,
		// already rejected by the strict Decode above), and any raw field
		// value that is exactly the null literal names a null setting.
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(body, &raw); err != nil || raw == nil {
			return c, path, fmt.Errorf("%s: must be a JSON object, not null", path)
		}
		for key, v := range raw {
			if bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
				return c, path, fmt.Errorf("%s: %q must not be null", path, key)
			}
		}
	}
	if err := c.validate(); err != nil {
		return c, path, fmt.Errorf("%s: %w", path, err)
	}
	base := filepath.Dir(path)
	c.DataDirectory, err = resolve(base, c.DataDirectory)
	if err != nil {
		return c, path, err
	}
	if c.EmbeddingAPIKeyFile != "" {
		c.EmbeddingAPIKeyFile, err = resolve(base, c.EmbeddingAPIKeyFile)
		if err != nil {
			return c, path, err
		}
	}
	if d := os.Getenv("AGENTBUS_DATA_DIR"); d != "" {
		c.DataDirectory = d
	}
	c.Path = path
	return c, path, nil
}

func resolve(base, p string) (string, error) {
	if strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		return filepath.Join(home, p[2:]), nil
	}
	if filepath.IsAbs(p) {
		return p, nil
	}
	return filepath.Join(base, p), nil
}

func (c *Config) validate() error {
	bounds := []struct {
		name     string
		v        int
		min, max int
	}{
		{"sqlite_budget_mib", c.SQLiteBudgetMiB, 64, 1048576},
		{"message_retention_hours", c.MessageRetentionHours, 1, 8760},
		{"cleanup_free_percent", c.CleanupFreePercent, 5, 90},
		{"cleanup_interval_seconds", c.CleanupIntervalSeconds, 5, 3600},
		{"cursor_idle_hours", c.CursorIdleHours, 1, 720},
		{"tombstone_min_hours", c.TombstoneMinHours, 72, 720},
		{"receipt_retention_minutes", c.ReceiptRetentionMinutes, 1, 1440},
		{"max_message_kib", c.MaxMessageKiB, 1, 1024},
		{"send_messages_per_second", c.SendMessagesPerSecond, 1, 10000},
		{"send_kib_per_second", c.SendKiBPerSecond, 64, 65536},
		{"receive_default_count", c.ReceiveDefaultCount, 1, 1000},
		{"receive_max_count", c.ReceiveMaxCount, 1, 10000},
		{"receive_max_wait_seconds", c.ReceiveMaxWaitSeconds, 0, 240},
		{"result_default_kib", c.ResultDefaultKiB, 1, 4096},
	}
	for _, b := range bounds {
		if b.v < b.min || b.v > b.max {
			return fmt.Errorf("%s must be between %d and %d, got %d", b.name, b.min, b.max, b.v)
		}
	}
	if c.DataDirectory == "" {
		return errors.New("data_directory must be nonempty")
	}
	if c.ReceiveDefaultCount > c.ReceiveMaxCount {
		return errors.New("receive_default_count must be <= receive_max_count")
	}
	if c.InspectionTimeoutSeconds < 0.1 || c.InspectionTimeoutSeconds > 60 {
		return fmt.Errorf("inspection_timeout_seconds must be between 0.1 and 60, got %v", c.InspectionTimeoutSeconds)
	}
	if c.EmbeddingQueryTimeoutSeconds < 0.1 || c.EmbeddingQueryTimeoutSeconds > 120 {
		return fmt.Errorf("embedding_query_timeout_seconds must be between 0.1 and 120, got %v", c.EmbeddingQueryTimeoutSeconds)
	}
	if len(c.InspectionCommand) > 64 {
		return errors.New("inspection_command may have at most 64 entries")
	}
	enc, err := json.Marshal(c.InspectionCommand)
	if err != nil {
		return fmt.Errorf("inspection_command: %w", err)
	}
	if len(enc) > 32*1024 {
		return errors.New("inspection_command exceeds 32 KiB encoded")
	}
	if c.EmbeddingEndpoint != "" {
		if c.EmbeddingModel == "" {
			return errors.New("embedding_model is required when embedding_endpoint is set")
		}
		u, err := url.Parse(c.EmbeddingEndpoint)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return fmt.Errorf("embedding_endpoint: must be an absolute URL with scheme and host, got %q", c.EmbeddingEndpoint)
		}
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("log_level must be one of debug, info, warn, error, got %q", c.LogLevel)
	}
	if c.TUIName == "" {
		return errors.New("tui_name must not be empty")
	}
	return nil
}
