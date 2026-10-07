package bus

import (
	"strings"
	"testing"
)

// A Windows absolute path has no leading slash, so as a URI path it must
// gain one: file:///C:/... rather than file://C:%5C..., which SQLite
// parses as an authority and rejects ("invalid uri authority").
func TestSQLiteURIPath(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"/Users/pat/agentbus.db", "/Users/pat/agentbus.db"},
		{"C:/Users/pat/agentbus.db", "/C:/Users/pat/agentbus.db"},
	} {
		if got := sqliteURIPath(tc.in); got != tc.want {
			t.Errorf("sqliteURIPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSQLiteDSNHasEmptyAuthority(t *testing.T) {
	dsn, err := SQLiteDSN(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(dsn, "file:///") || strings.Contains(dsn, `%5C`) {
		t.Fatalf("dsn = %q, want file:/// and no escaped backslashes", dsn)
	}
}
