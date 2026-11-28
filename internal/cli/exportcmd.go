package cli

import (
	"encoding/json"
	"io"
	"log/slog"

	"github.com/ericfitz/agentbus/internal/bus"
	"github.com/ericfitz/agentbus/internal/config"
)

// ExportMemories is `agentbus export-memories`: one JSON object per line.
func ExportMemories(cfg config.Config, prunedOnly bool, out io.Writer) error {
	b, err := bus.Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		return err
	}
	defer func() { _ = b.Close() }()
	list, err := b.ExportMemories(prunedOnly)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(out)
	for _, m := range list {
		if err := enc.Encode(m); err != nil {
			return err
		}
	}
	return nil
}
