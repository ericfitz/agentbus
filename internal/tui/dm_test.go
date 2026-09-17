package tui

import (
	"strings"
	"testing"

	"github.com/ericfitz/agentbus/internal/bus"
)

func TestRailHidesDMChannelsAndCountsSessionUnread(t *testing.T) {
	f := newFixture(t)
	f.run(f.m.statusCmd()) // learn Sam's session and the DM channels
	for _, c := range f.m.channels {
		if strings.HasPrefix(c.Name, bus.DMPrefix) {
			t.Fatalf("DM channel in the channel list: %s", c.Name)
		}
	}
	if len(f.m.dms) < 2 { // dm/Sam and dm/eric
		t.Fatalf("dms=%v", f.m.dms)
	}
	f.agentSend(t, "dm/Sam", "note to self") // Sam -> own inbox; any sender works
	f.receive(t)
	if got := f.m.unread("dm/Sam"); got != 1 {
		t.Fatalf("unread(dm/Sam)=%d", got)
	}
	rail := f.m.renderRails()
	var samRow string
	for _, l := range strings.Split(rail, "\n") {
		if strings.Contains(l, "Sam") {
			samRow = l
		}
	}
	if !strings.Contains(samRow, "1") {
		t.Fatalf("session row must show its unread DM count: %q", samRow)
	}
}
