//go:build race

package mcpserver

// raceEnabled is true when this test binary was itself built with -race
// (go build/test set the implicit "race" build tag in that case). TestMain
// uses it to build the spawned `agentbus mcp` child binary with -race too,
// so integration tests give the child real race coverage instead of only
// covering the test harness.
const raceEnabled = true
