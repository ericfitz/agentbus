package cli

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestHelperSleep is not a test: sleeperCommand runs this test binary with
// AGENTBUS_TEST_HELPER=sleep so tests have a live child to inspect on every
// OS (Windows has no `sleep`). It is skipped in a normal run.
func TestHelperSleep(t *testing.T) {
	if os.Getenv("AGENTBUS_TEST_HELPER") != "sleep" {
		t.Skip("helper process entry point")
	}
	time.Sleep(60 * time.Second)
}

// sleeperCommand is a child process that lives for 60 s unless killed.
func sleeperCommand() *exec.Cmd {
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperSleep$")
	cmd.Env = append(os.Environ(), "AGENTBUS_TEST_HELPER=sleep")
	return cmd
}
