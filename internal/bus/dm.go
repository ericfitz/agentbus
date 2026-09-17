package bus

import (
	"database/sql"
	"strings"
)

// DMPrefix starts the name of every direct-message inbox. validateName
// rejects '/', so no user-created channel can carry it; DM-ness is derived
// from the name alone and needs no schema support.
const DMPrefix = "dm/"

// DMChannel is the inbox channel of identity.
func DMChannel(identity string) string { return DMPrefix + identity }

// dmOwner reports the identity that owns a DM inbox channel: everything
// after the first "dm/", so a subagent "Sam/impl" owns "dm/Sam/impl".
func dmOwner(channel string) (string, bool) {
	owner, ok := strings.CutPrefix(channel, DMPrefix)
	return owner, ok && owner != ""
}

// SetObserver lets identity as, registered in this process, read and
// subscribe to every DM inbox. Only the in-process TUI calls it; the MCP
// server never does, so no agent can obtain it through a tool.
func (b *Bus) SetObserver(as string) { b.observer = as }

// isObserver reports whether as is this process's observer. An empty as is
// never an observer even if b.observer is also empty (bus.Wait has no auth
// and passes as through unchecked, so this must not accidentally match "").
func (b *Bus) isObserver(as string) bool {
	return as != "" && b.observer == as
}

// dmReadable reports whether as may read channel: always for ordinary
// channels, and for a DM inbox only its owner or the observer. Enforced at
// subscribe, history, search, and delivery (receive, wait, register's
// pending list).
func (b *Bus) dmReadable(as, channel string) bool {
	owner, ok := dmOwner(channel)
	return !ok || owner == as || b.isObserver(as)
}

// ensureInbox creates display's inbox and its subscription to it, both
// idempotently, inside Register's transaction. A new subscription starts at
// 0 so DMs sent before a resume=false re-register are still delivered.
func (b *Bus) ensureInbox(tx *sql.Tx, display string, now int64) error {
	ch := DMChannel(display)
	if _, err := tx.Exec("INSERT OR IGNORE INTO channels(name,kind,created_seq,evicted_before_seq) VALUES(?,'ordinary',(SELECT coalesce(max(seq),0) FROM messages),0)", ch); err != nil {
		return internal(err)
	}
	if _, err := tx.Exec("INSERT OR IGNORE INTO subscriptions(sender,channel,cursor_seq,last_activity) VALUES(?,?,0,?)", display, ch, now); err != nil {
		return internal(err)
	}
	return nil
}
