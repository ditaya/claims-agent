# claims-agent

A Claude-powered insurance claims triage agent in Go, built directly on the
[official `anthropic-sdk-go`](https://github.com/anthropics/anthropic-sdk-go) - no agent
framework. The tool-use loop (send claim → Claude requests a tool → execute it locally →
feed the result back → repeat) is hand-written in [`internal/agent/agent.go`](internal/agent/agent.go)
so the mechanics are visible end to end.

## Approach

The agent is given a claim as free text and three investigative tools - `lookup_policy`,
`check_coverage`, `flag_fraud_signals` - backed by a 10-policy seed file
([`data/policies.json`](data/policies.json)). It must gather what it needs and then call a
fourth tool, `submit_decision`, exactly once to end the loop. `submit_decision` isn't a
"real" tool with a side effect - the agent loop treats it as the terminal signal and
returns its input directly as the final `Decision`, rather than parsing free-form text or
relying on a second structured-output pass. This makes the final answer schema-shaped by
construction instead of by hope.

The loop is bounded two ways: a hard cap on tool-use round trips (`-max-iterations`,
default 8) and a context timeout (`-timeout`, default 60s). Hitting either one does not
fail the request - it returns an `"escalate"` decision explaining why, because guessing at
a payout is worse than asking a human to look. The same "escalate, don't guess" rule
applies if Claude ends the turn without calling `submit_decision`, or references a policy
number that doesn't exist.

## Architecture & why

```
cmd/claims-agent/        CLI entrypoint: `triage` and `serve` subcommands
internal/
  agent/                 the hand-written tool-use loop, Decision/Step types, submit_decision
  claude/                claude.Client interface + real SDK-backed impl + scriptable Fake
  tools/                 lookup_policy, check_coverage, flag_fraud_signals + their Registry
  policy/                loads/queries the seeded policy book
  httpapi/               POST /triage handler (stdlib net/http only)
data/policies.json       10 seeded policies
evals/                   12 sample claims + expected decisions, and a runner that scores them
```

**`claude.Client` is a one-method interface** (`CreateMessage(ctx, params) (*anthropic.Message, error)`)
wrapping the SDK's real request/response types rather than inventing parallel ones. That
keeps the production path a thin pass-through to `client.Messages.New`, while
`claude.Fake` (in `internal/claude/fake.go`, not a `_test.go` file so other packages'
tests can import it) scripts a sequence of responses for the agent loop to walk through
with no network calls. `internal/agent`, `internal/httpapi`, and `internal/tools` are all
tested this way - `go test ./...` never touches the network or costs money.

**`submit_decision` as a tool, not parsed text.** An earlier design had the agent just
write its final answer as a JSON blob in the last text turn. Driving it through a tool
instead means the SDK's own tool-use machinery enforces the shape (`decision` is one of
three enum values, `payout` is a number, `reasoning` is required) - there's no regex or
"strip the markdown fences" step, and a malformed final answer surfaces as a clear parse
error on `submit_decision`'s input rather than a silently-wrong text scrape.

**Manual loop, not the SDK's tool runner.** The SDK ships a beta `BetaToolRunner` that
automates this exact loop. This project writes it by hand per the brief - see the
iteration loop in `Agent.Triage` - which also means no beta API dependency.

**HTTP on stdlib only.** `net/http`'s `ServeMux` has supported method-prefixed patterns
(`"POST /triage"`) since Go 1.22, which was enough routing for one endpoint - no router
dependency.

## Assumptions

- **Go version:** the brief asked for Go 1.23, but every current `anthropic-sdk-go`
  release (back through at least v1.70) declares `go 1.24` in its own `go.mod`, and `go
  get` bumps the importing module to match. `go.mod` here says `go 1.24`; there's nothing
  else Go-1.23-specific in the code.
- **Claim input is free text**, not structured fields. The agent is expected to extract
  the policy number, loss type, amount, and date itself and pass them to the tools -
  this is part of what's being triaged, not pre-parsed for it.
- **`flag_fraud_signals` is a deliberately simple, deterministic heuristic** (keyword
  matching, round-number amounts, early-claim timing) - see "What I'd add for production"
  below for what a real version would need.
- **One claim = one triage = one conversation.** There's no persistence across requests;
  `POST /triage` and `claims-agent triage` are both stateless, single-shot calls.
- **The policy book is static and in-memory**, loaded once from JSON at startup. No
  database, no concurrent-write concerns.
- **Model default is `claude-opus-5`**, overridable with `-model` on every subcommand.

## Running it

```bash
export ANTHROPIC_API_KEY=sk-ant-...
go build ./...

# CLI
go run ./cmd/claims-agent triage "Policy POL-1001, rear-ended on 2026-09-10, $2,000 in damage."

# HTTP
go run ./cmd/claims-agent serve -addr :8080
curl -X POST localhost:8080/triage -d '{"claim":"Policy POL-1001, rear-ended on 2026-09-10, $2,000 in damage."}'
```

Both default to `data/policies.json` relative to the working directory; override with
`-policies`. Run `go run ./cmd/claims-agent triage -h` (or `serve -h`) for all flags.

### Docker

```bash
docker build -t claims-agent .
docker run --rm -p 8080:8080 -e ANTHROPIC_API_KEY claims-agent
```

(Not verified in this environment - no Docker daemon was available while building this -
but it's a standard two-stage Alpine build; flag it if it doesn't come up clean.)

## Running the evals

```bash
go run ./evals/runner
```

This sends all 12 claims in [`evals/claims.json`](evals/claims.json) through the real
agent (12 live API calls - costs money, isn't run in CI) and prints a table:

```
ID   EXPECTED  GOT       MATCH  PAYOUT    LATENCY
C01  approve   approve   PASS   1500.00   11.675s
...
12/12 passed
```

The runner exits non-zero if any case mismatches, so it can be wired into a manual release
check. `-model`, `-policies`, `-claims`, `-max-iterations`, and `-timeout` all override
the defaults, same as the CLI.

The 12 cases were chosen to cover each decision roughly evenly: 4 clean approvals, 4
clear-cut denials (lapsed/cancelled policy, two different explicit exclusions), and 4
escalations (an ambiguous early/high-value claim with vague language, an unknown policy
number, heavy fraud-signal language, and a self-contradictory claimant story).

## CI

[`.github/workflows/ci.yml`](.github/workflows/ci.yml) runs `go vet ./...` and
`go test ./... -v` on every push and PR. It deliberately does **not** run the evals -
they call the real API, cost money, and would need an `ANTHROPIC_API_KEY` secret and a
tolerance for LLM-call latency in CI. See "What I'd add for production" for how I'd wire
that in properly.

## What I'd add for production

- **Evals in CI, gated and budgeted.** A scheduled (nightly, not per-PR) workflow that
  runs `evals/runner` against a repo secret, posts the pass rate somewhere visible, and
  fails loudly on regression - with a fixed model version pin so a silent model update
  doesn't change the baseline underneath you.
- **A real fraud-detection backend.** `flag_fraud_signals` today is keyword/heuristic
  only. Production would want historical claim-pattern lookups, cross-claim duplicate
  detection, and ideally a model or service trained for this rather than substring
  matching.
- **Structured claim intake upstream.** Letting Claude parse policy number/amount/date out
  of free text is fine for a triage demo; a real intake form would pass those fields
  explicitly and use the LLM for judgment, not extraction, to remove a whole class of
  misparse risk.
- **Persistence and audit trail.** Every `Decision.Steps` trace is currently returned and
  discarded. Production would persist every triage (inputs, tool calls, final decision,
  model/version used) for compliance review and appeal handling.
- **Human-in-the-loop for `escalate`.** Right now "escalate" is a dead end - there's no
  queue, no adjuster UI, no notification. That's the other half of this system. The same
  goes for `approve` + payout: this agent recommends a payout, it doesn't disburse money -
  a real payout path needs its own authorization step independent of the agent's output.
- **Confidence-aware approval thresholds.** E.g., auto-approve only below a payout
  ceiling, and always route anything above it to escalate regardless of what the model
  says - a policy decision, not a prompt tweak.
- **Retry/backoff and rate-limit handling** around the Claude call in `claude.AnthropicClient`
  - the SDK retries 429/5xx automatically, but a production queue would want its own
    circuit breaker rather than blocking the request thread.
- **Observability**: structured logs per tool call and per decision, latency/cost metrics
  per triage, and an alert on an unexpected spike in `escalate` rate (a leading indicator
  that either fraud is up or the prompt/tools regressed).
