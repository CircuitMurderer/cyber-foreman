# Cyber Foreman contributor guide

## Architecture

- Keep agent-specific behavior behind `internal/agent.Adapter`.
- Prefer structured protocols (ACP, JSON-RPC, JSONL, hooks) over terminal scraping.
- Put lifecycle facts in `internal/domain` and legal transitions in `internal/task`.
- Deterministic safety and recovery rules belong in `internal/supervisor`.
- The control plane must remain usable with an internal, OpenAI-compatible model endpoint.

## Development

- Use `./scripts/go` instead of relying on a globally installed Go toolchain.
- Run `./scripts/test` before handing off changes.
- Prefer the Go standard library until a dependency removes substantial complexity.
- Keep the default HTTP listener on loopback; authentication is required before exposing it to a network.
