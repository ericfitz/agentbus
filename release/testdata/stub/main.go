// Command stub stands in for agentbus.exe in release/test-install.ps1:
// `stub version` prints the version linked in, `stub sleep` stays alive so a
// test can upgrade while the old binary runs.
package main

import (
	"fmt"
	"os"
	"time"
)

var version = "0.0.0"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "sleep" {
		time.Sleep(60 * time.Second)
		return
	}
	fmt.Println(version)
}
