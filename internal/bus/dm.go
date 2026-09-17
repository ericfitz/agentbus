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
