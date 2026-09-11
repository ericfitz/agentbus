package tui

import (
	"io"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ericfitz/agentbus/internal/config"
	"github.com/ericfitz/agentbus/internal/mcpserver"
)

// Run starts the dashboard as identity `as` (empty means cfg.TUIName) and
// returns when the user quits. Theme warnings go to stderr before the
// alternate screen opens; bus logs go to the shared log file.
func Run(cfg config.Config, as string, stderr io.Writer) error {
	if as == "" {
		as = cfg.TUIName
	}
	log, err := mcpserver.OpenLog(cfg)
	if err != nil {
		return err
	}
	theme := LoadTheme(cfg, stderr)
	c, err := newClient(cfg, as, log)
	if err != nil {
		return err
	}
	p := tea.NewProgram(New(c, theme), tea.WithAltScreen())
	go c.receiveLoop(p.Send)
	_, runErr := p.Run()
	closeErr := c.close()
	if runErr != nil {
		return runErr
	}
	return closeErr
}
