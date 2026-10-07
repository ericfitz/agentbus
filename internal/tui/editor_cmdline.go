package tui

// cmdExeCommandLine is the cmd.exe command line that runs editor setting ed
// on path. With /S, cmd strips exactly the outer quotes, so a quoted program
// path with spaces and a quoted file path both survive. It is plain string
// building, kept out of the Windows-only file so every OS tests it.
func cmdExeCommandLine(ed, path string) string {
	return `cmd.exe /S /C "` + ed + ` "` + path + `""`
}
