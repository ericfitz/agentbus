package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/ericfitz/agentbus/internal/bus"
	"github.com/ericfitz/agentbus/internal/config"
)

// SetStatusCmd is `agentbus set-status` (issue #43): it records a status for
// the session this process runs for, or for as when given. The session must
// be live.
func SetStatusCmd(cfg config.Config, as, cwd, tag, text, problem string, out, warn io.Writer) error {
	b, err := bus.Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		return err
	}
	defer func() { _ = b.Close() }()
	name := parkIdentity(b, as, cwd, warn)
	if name == "" {
		return ErrNoIdentity
	}
	st, err := b.SetStatusFor(name, tag, text, problem)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "status for %s: revision %d\n", name, st.Revision)
	return err
}

// StatusSortModes are the values of `statuses -sort`.
var StatusSortModes = []string{"time", "name", "tag"}

// tagLabel is the tag as the table shows it: — for a never-set status, which
// sorts after every ASCII tag.
func tagLabel(e bus.BoardEntry) string {
	if e.Tag == "" {
		return "—"
	}
	return e.Tag
}

// SortBoard orders e in place by "time" (newest first; never-set entries,
// SetAt 0, last), "name", or "tag" (alphabetical by the displayed tag). Ties
// break by name. Any other by is an error.
func SortBoard(e []bus.BoardEntry, by string) error {
	var less func(a, b bus.BoardEntry) (lt, eq bool)
	switch by {
	case "time":
		less = func(a, b bus.BoardEntry) (bool, bool) { return a.SetAt > b.SetAt, a.SetAt == b.SetAt }
	case "name":
		less = func(a, b bus.BoardEntry) (bool, bool) { return false, true }
	case "tag":
		less = func(a, b bus.BoardEntry) (bool, bool) { return tagLabel(a) < tagLabel(b), tagLabel(a) == tagLabel(b) }
	default:
		return fmt.Errorf("unknown sort %q; use one of %s", by, strings.Join(StatusSortModes, ", "))
	}
	sort.SliceStable(e, func(i, j int) bool {
		if lt, eq := less(e[i], e[j]); !eq {
			return lt
		}
		return e[i].Identity < e[j].Identity
	})
	return nil
}

// Statuses is `agentbus statuses`: the board (live sessions plus recently
// offline ones) as a table or JSON, or with a name that name's status
// history, oldest first.
func Statuses(cfg config.Config, name, sortBy string, asJSON bool, out io.Writer) error {
	if sortBy == "" {
		sortBy = "time"
	}
	if err := SortBoard(nil, sortBy); err != nil {
		return err
	}
	b, err := bus.Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		return err
	}
	defer func() { _ = b.Close() }()
	now := b.Now()
	if name != "" {
		hist, _, err := b.StatusHistory(name)
		if err != nil {
			return err
		}
		if asJSON {
			return json.NewEncoder(out).Encode(hist)
		}
		entries := make([]bus.BoardEntry, len(hist))
		for i, h := range hist {
			entries[i] = bus.BoardEntry{AgentStatus: h}
		}
		return writeStatusTable(out, entries, now, false)
	}
	board, err := b.Board()
	if err != nil {
		return err
	}
	if err := SortBoard(board, sortBy); err != nil {
		return err
	}
	if asJSON {
		return json.NewEncoder(out).Encode(board)
	}
	return writeStatusTable(out, board, now, true)
}

// writeStatusTable prints e as columns tag, name, age, text, problem.
func writeStatusTable(out io.Writer, e []bus.BoardEntry, now time.Time, header bool) error {
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	if header {
		_, _ = fmt.Fprintln(tw, "TAG\tNAME\tAGE\tTEXT\tPROBLEM")
	}
	for _, x := range e {
		tag, age := tagLabel(x), "-"
		if x.SetAt > 0 {
			age = bus.ShortAge(now.Sub(time.UnixMilli(x.SetAt)))
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", tag, x.Identity, age, x.Text, x.Problem)
	}
	return tw.Flush()
}
