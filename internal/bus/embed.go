package bus

import "github.com/ericfitz/agentbus-local/internal/config"

// embedder is a stub until Task 9 implements real embedding.
type embedder struct{}

func newEmbedder(config.Config) (*embedder, error) { return &embedder{}, nil }
