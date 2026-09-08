package bus

import "testing"

func TestResetWipesAndLiveProcessGetsNotRegistered(t *testing.T) {
	b := newTestBus(t)
	sam := reg(t, b, "Sam")
	b.CreateChannel(sam, "dev", "ordinary")
	b.Send(sam, SendInput{Channel: "dev", Content: "x"})
	admin, _ := Open(b.cfg, b.log)
	defer admin.Close()
	if n, _ := admin.LiveSessionCount(); n != 1 {
		t.Fatal(n)
	}
	if err := admin.Reset(); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Send(sam, SendInput{Channel: "dev", Content: "y"}); err == nil {
		t.Fatal("live process must lose registration after reset")
	}
	r, _ := b.Register("Sam", "", "", true)
	if r.Sender != "Sam" || r.Resumed {
		t.Fatalf("%+v", r)
	}
	b.CreateChannel("Sam", "dev", "ordinary")
	s, _ := b.Send("Sam", SendInput{Channel: "dev", Content: "z"})
	if s.Seq != 1 {
		t.Fatalf("sequence must restart after reset, got %d", s.Seq)
	}
	st, _ := b.StatusReport()
	if len(st.Sessions) != 1 || len(st.Channels) != 1 || st.UsageBytes <= 0 {
		t.Fatalf("%+v", st)
	}
}
