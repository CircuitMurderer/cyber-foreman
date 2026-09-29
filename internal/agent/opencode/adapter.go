package opencode

import (
	"time"

	"cyber-foreman/internal/agent/acpagent"
)

var ErrUnknownSession = acpagent.ErrUnknownSession

// Config is kept for compatibility with the original OpenCode-specific
// adapter. Args are inserted before the final `acp` subcommand, which keeps
// helper-process tests and existing callers working.
type Config struct {
	Binary           string
	Args             []string
	Env              []string
	HandshakeTimeout time.Duration
	GracePeriod      time.Duration
}

func NewAdapter(config Config) (*acpagent.Adapter, error) {
	profile := acpagent.OpenCodeProfile(config.Binary)
	profile.Args = append(append([]string(nil), config.Args...), profile.Args...)
	return acpagent.NewAdapter(acpagent.Config{
		Profile: profile, Env: config.Env,
		HandshakeTimeout: config.HandshakeTimeout, GracePeriod: config.GracePeriod,
	})
}
