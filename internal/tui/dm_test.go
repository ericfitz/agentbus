package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
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

// The highlighted row follows focus between the channel list and the
// sessions pane: only one rail ever shows a selected row at a time.
func TestRailHighlightFollowsFocusBetweenPanes(t *testing.T) {
	f := newFixture(t)
	f.run(f.m.statusCmd()) // learn Sam's session
	f.key("esc")
	channelRow := func() string { return strings.SplitN(f.m.renderRails(), "\n", 3)[1] }
	sessionRow := func() string {
		rail := f.m.renderRails()
		for _, l := range strings.Split(rail, "\n") {
			if strings.Contains(l, "Sam") {
				return l
			}
		}
		t.Fatal("no session row for Sam")
		return ""
	}
	if !strings.Contains(channelRow(), markSel) {
		t.Fatalf("channel list must be highlighted while it holds the selection: %q", channelRow())
	}
	if strings.Contains(sessionRow(), markSel) {
		t.Fatalf("sessions pane must not be highlighted while the channel list holds the selection: %q", sessionRow())
	}
	f.toSessions()
	if strings.Contains(channelRow(), markSel) {
		t.Fatalf("channel list must lose its highlight once the sessions pane holds the selection: %q", channelRow())
	}
	if !strings.Contains(sessionRow(), markSel) {
		t.Fatalf("sessions pane must be highlighted once it holds the selection: %q", sessionRow())
	}
}

func TestSessionsPaneShowsInboxAndComposesDM(t *testing.T) {
	f := newFixture(t)
	f.run(f.m.statusCmd())
	f.key("esc")
	f.toSessions()
	if f.m.pane() != paneSessions {
		t.Fatalf("pane=%v", f.m.pane())
	}
	names := f.m.sessionNames()
	for f.m.sessSel < len(names)-1 && names[f.m.sessSel] != "Sam" {
		f.key("down")
	}
	if f.m.selName() != "dm/Sam" {
		t.Fatalf("selected=%q", f.m.selName())
	}
	if h := f.m.renderHeader(); !strings.Contains(h, "@Sam") || !strings.Contains(h, "direct") {
		t.Fatalf("header=%q", h)
	}
	f.key("i")
	for _, r := range "hello sam" {
		f.key(string(r))
	}
	f.key("enter")
	ms, err := f.ab.History(f.sam, "dm/Sam", nil, nil, 10)
	if err != nil || len(ms) != 1 || ms[0].Content != "hello sam" || ms[0].Sender != f.c.as {
		t.Fatalf("%+v %v", ms, err)
	}
	f.key("esc")
	f.key("home")
	if f.m.pane() != paneChannels || strings.HasPrefix(f.m.selName(), "dm/") {
		t.Fatalf("home must return to the channel list: pane=%v sel=%q", f.m.pane(), f.m.selName())
	}
}

func TestReplyOnlyInOwnInbox(t *testing.T) {
	f := newFixture(t)
	f.run(f.m.statusCmd())
	f.agentSend(t, "dm/"+f.c.as, "which oauth provider?")
	f.agentSend(t, "dm/Sam", "note")
	f.receive(t)
	f.key("esc")
	f.toSessions()
	sel := func(name string) {
		for i, n := range f.m.sessionNames() {
			if n == name {
				for f.m.sessSel < i {
					f.key("down")
				}
				for f.m.sessSel > i {
					f.key("up")
				}
			}
		}
	}
	sel("Sam")
	f.key("r")
	if f.m.replyTo != nil || f.m.toast == "" {
		t.Fatalf("reply must be refused in another identity's inbox: replyTo=%v toast=%q", f.m.replyTo, f.m.toast)
	}
	f.key("x") // clears the toast
	sel(f.c.as)
	f.key("r")
	if f.m.replyTo == nil {
		t.Fatal("reply must work in the TUI's own inbox")
	}
	for _, r := range "github" {
		f.key(string(r))
	}
	f.key("enter")
	ms, _ := f.ab.History(f.sam, "dm/Sam", nil, nil, 10)
	last := ms[len(ms)-1]
	if last.Content != "github" || last.ReplyTo == nil {
		t.Fatalf("reply must land in the sender's inbox with reply_to: %+v", last)
	}
}

// A reply whose parent lives in another channel renders as a top-level row.
func TestCrossChannelReplyRendersTopLevel(t *testing.T) {
	f := newFixture(t)
	parent := f.agentSend(t, "dev", "parent in dev")
	if _, err := f.ab.Send(f.sam, bus.SendInput{Channel: "dm/" + f.c.as, Content: "child elsewhere", ReplyTo: &parent.Seq}); err != nil {
		t.Fatal(err)
	}
	f.run(f.m.statusCmd())
	f.receive(t)
	rs := f.m.rows("dm/" + f.c.as)
	if len(rs) != 1 || rs[0].depth != 0 || rs[0].msg.Content != "child elsewhere" {
		t.Fatalf("%+v", rs)
	}
}

// dmSetup registers a second agent and returns a sender for each identity:
// the TUI ("eric"), Sam, and Kim, all on the same bus file.
func (f *fixture) selectSessionNamed(t *testing.T, name string) {
	t.Helper()
	f.key("esc")
	f.toSessions()
	for i, n := range f.m.sessionNames() {
		if n == name {
			f.run(f.m.selectSession(i))
			return
		}
	}
	t.Fatalf("no session %q in %v", name, f.m.sessionNames())
}

func (f *fixture) sendAs(t *testing.T, b *bus.Bus, as, ch string, replyTo *int64, content string) bus.SendResult {
	t.Helper()
	r, err := b.Send(as, bus.SendInput{Channel: ch, Content: content, ReplyTo: replyTo})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// A two-agent back-and-forth is one thread in both panes even though every
// other message lives in the other inbox.
func TestDMPaneThreadsConversationAcrossInboxes(t *testing.T) {
	f := newFixture(t)
	f.run(f.m.statusCmd())
	q1 := f.sendAs(t, f.c.b, f.c.as, "dm/Sam", nil, "q1")
	a1 := f.sendAs(t, f.ab, f.sam, "dm/"+f.c.as, &q1.Seq, "a1")
	q2 := f.sendAs(t, f.c.b, f.c.as, "dm/Sam", &a1.Seq, "q2")
	f.sendAs(t, f.ab, f.sam, "dm/"+f.c.as, &q2.Seq, "a2")
	f.receive(t)
	for _, seq := range []int64{q1.Seq, a1.Seq, q2.Seq} {
		f.m.expanded[seq] = true
	}
	want := []string{"q1", ">a1", ">>q2", ">>>a2"}
	if got := contents(f.m.rows("dm/Sam")); !eq(got, want) {
		t.Fatalf("Sam's pane: %v", got)
	}
	if got := contents(f.m.rows("dm/" + f.c.as)); !eq(got, want) {
		t.Fatalf("eric's pane: %v", got)
	}
	f.selectSessionNamed(t, "Sam")
	// This is Sam's pane's first-ever visit with content already loaded, so
	// (per Task 3) a "new" divider sits ahead of the header; find the header
	// by content rather than assuming it is line 0.
	var first string
	for _, l := range strings.Split(ansi.Strip(f.m.renderStream()), "\n") {
		if strings.Contains(l, "-->") {
			first = l
			break
		}
	}
	if !strings.Contains(first, f.c.as+" --> ") || !strings.Contains(first, "Sam") {
		t.Fatalf("header shows the real direction: %q", first)
	}
}

// A pane that mixes two partners lists each conversation; unread counts and
// the divider come from the inbox alone.
func TestDMPaneMixesPartnersAndCountsInboxOnly(t *testing.T) {
	f := newFixture(t)
	kb, kim := agent(t, f.c.cfg, "Kim")
	f.run(f.m.statusCmd())
	f.sendAs(t, f.ab, f.sam, "dm/"+f.c.as, nil, "from sam")
	f.sendAs(t, kb, kim, "dm/"+f.c.as, nil, "from kim")
	f.sendAs(t, f.c.b, f.c.as, "dm/Kim", nil, "to kim")
	f.receive(t)
	if got := contents(f.m.rows("dm/" + f.c.as)); !eq(got, []string{"from sam", "from kim", "to kim"}) {
		t.Fatalf("eric's pane: %v", got)
	}
	if got := contents(f.m.rows("dm/Kim")); !eq(got, []string{"from kim", "to kim"}) {
		t.Fatalf("Kim's pane: %v", got)
	}
	if got := contents(f.m.rows("dm/Sam")); !eq(got, []string{"from sam"}) {
		t.Fatalf("Sam's pane: %v", got)
	}
	if f.m.unread("dm/Kim") != 1 || f.m.unread("dm/"+f.c.as) != 2 {
		t.Fatalf("unread counts inbox only: kim=%d eric=%d", f.m.unread("dm/Kim"), f.m.unread("dm/"+f.c.as))
	}
	f.selectSessionNamed(t, "Kim")
	s := ansi.Strip(f.m.renderStream())
	// "from kim" is Kim's own outgoing message (not unread); the divider
	// sits after it and before "to kim", the first unread inbox message.
	if strings.Count(s, " new ") != 1 || strings.Index(s, " new ") < strings.Index(s, "from kim") || strings.Index(s, " new ") > strings.Index(s, "to kim") {
		t.Fatalf("divider sits before the first unread inbox message:\n%s", s)
	}
}

// Selecting a DM pane loads history for every inbox, so outgoing messages
// sent before the TUI started still show.
func TestDMPaneLoadsOtherInboxHistory(t *testing.T) {
	f := newFixture(t)
	f.sendAs(t, f.c.b, f.c.as, "dm/Sam", nil, "earlier")
	f.drainAndAck(t)
	f.run(f.m.statusCmd())
	f.selectSessionNamed(t, f.c.as)
	if got := contents(f.m.rows("dm/" + f.c.as)); !eq(got, []string{"earlier"}) {
		t.Fatalf("own pane after history: %v", got)
	}
}

// r on the TUI's own outgoing row replies to the partner, not to dm/self.
func TestReplyFromOwnOutgoingRowTargetsPartner(t *testing.T) {
	f := newFixture(t)
	f.run(f.m.statusCmd())
	f.sendAs(t, f.c.b, f.c.as, "dm/Sam", nil, "ping")
	f.receive(t)
	f.selectSessionNamed(t, f.c.as)
	f.key("r") // no cursor: replies to the last row, our own "ping"
	for _, r := range "pong" {
		f.key(string(r))
	}
	f.key("enter")
	ms, _ := f.ab.History(f.sam, "dm/Sam", nil, nil, 10)
	if len(ms) != 2 || ms[1].Content != "pong" || ms[1].ReplyTo == nil || *ms[1].ReplyTo != ms[0].Seq {
		t.Fatalf("reply must land in Sam's inbox threaded on ping: %+v", ms)
	}
}
