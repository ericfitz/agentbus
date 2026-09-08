package bus

import "github.com/ericfitz/agentbus-local/internal/config"

// limiter is a stub until Task 5 implements real rate limiting.
type limiter struct{}

func newLimiter(config.Config) *limiter { return &limiter{} }
