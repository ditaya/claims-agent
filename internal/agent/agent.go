// Package agent implements the claims triage loop: send the claim to
// Claude with the tool set, execute whatever tools it calls, feed the
// results back, and repeat until it calls submit_decision (or the loop runs
// out of iterations/time, in which case the claim is escalated rather than
// guessed at).
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/anthropics/anthropic-sdk-go"

	"claims-agent/internal/claude"
	"claims-agent/internal/tools"
)

const (
	defaultModel         = "claude-opus-5"
	defaultMaxIterations = 8
	defaultTimeout       = 60 * time.Second
	defaultMaxTokens     = 4096
)

const systemPrompt = `You are an insurance claims triage agent. You will be given a claim as free
text from an intake form or call transcript. Your job is to investigate it using the
available tools and then submit exactly one final decision.

Process:
1. If the claim mentions a policy number, call lookup_policy to confirm it exists and
   check its status.
2. Call check_coverage to determine whether the claimed loss is covered under that
   policy, given its status, exclusions, limit, and deductible. Pass the incident date
   (in YYYY-MM-DD format) if the claim states or implies one, so it can be checked
   against the policy's effective/expiration window.
3. Call flag_fraud_signals to check for fraud risk signals in the claim description.
4. Once you have enough information, call submit_decision exactly once with your
   final decision. Do not call any other tool afterward.

Decision guidance:
- "approve": the claim is covered, the policy is active, and fraud risk is low.
  Set payout to the max_payout from check_coverage (or less, at your judgment).
- "deny": the claim is not covered (excluded peril, inactive policy, etc). Set payout to 0.
- "escalate": the case is ambiguous, borderline, high-value, or has meaningful fraud
  signals that need a human adjuster's judgment. Set payout to 0.

Always ground your decision in the tool results, and explain your reasoning referencing
what the tools returned. If a policy number can't be found or doesn't match any lookup,
escalate rather than guessing.`

// Decision is the final output of a triage: what to do with the claim, how
// much to pay (if anything), why, and the full trace of tool calls that led
// there.
type Decision struct {
	Decision  string  `json:"decision"` // approve | deny | escalate
	Payout    float64 `json:"payout"`
	Reasoning string  `json:"reasoning"`
	Steps     []Step  `json:"steps"`
}

// Step is one tool call in the trace: what was called, with what input, and
// what it returned.
type Step struct {
	Tool    string          `json:"tool"`
	Input   json.RawMessage `json:"input"`
	Output  string          `json:"output"`
	IsError bool            `json:"is_error,omitempty"`
}

// Agent runs the triage loop against a claude.Client and a tools.Registry.
type Agent struct {
	client        claude.Client
	registry      *tools.Registry
	model         anthropic.Model
	maxIterations int
	timeout       time.Duration
	maxTokens     int64
}

// Option configures an Agent.
type Option func(*Agent)

// WithModel overrides the default model (claude-opus-5).
func WithModel(model string) Option {
	return func(a *Agent) { a.model = anthropic.Model(model) }
}

// WithMaxIterations overrides the default max tool-use round trips (8)
// before the agent gives up and escalates.
func WithMaxIterations(n int) Option {
	return func(a *Agent) { a.maxIterations = n }
}

// WithTimeout overrides the default per-triage context timeout (60s).
func WithTimeout(d time.Duration) Option {
	return func(a *Agent) { a.timeout = d }
}

// New builds an Agent. client is typically *claude.AnthropicClient in
// production or *claude.Fake in tests; registry supplies lookup_policy,
// check_coverage, and flag_fraud_signals.
func New(client claude.Client, registry *tools.Registry, opts ...Option) *Agent {
	a := &Agent{
		client:        client,
		registry:      registry,
		model:         defaultModel,
		maxIterations: defaultMaxIterations,
		timeout:       defaultTimeout,
		maxTokens:     defaultMaxTokens,
	}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

// submitDecisionTool is the terminal tool the model must call to end the
// loop. It is not in tools.Registry because it has no side effect to
// execute - the agent loop itself interprets its input as the final Decision
// and stops, rather than round-tripping a tool_result for it.
func submitDecisionTool() anthropic.ToolUnionParam {
	def := anthropic.ToolParam{
		Name: "submit_decision",
		Description: anthropic.String("Submit your final triage decision for this claim. Call this exactly " +
			"once, after you've gathered enough information via lookup_policy, check_coverage, and " +
			"flag_fraud_signals. Do not call any other tool after this."),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{
				"decision": map[string]any{
					"type": "string",
					"enum": []string{"approve", "deny", "escalate"},
					"description": "approve: pay the claim. deny: reject the claim. escalate: send to a " +
						"human adjuster because the case is ambiguous, high-risk, or high-value.",
				},
				"payout": map[string]any{
					"type":        "number",
					"description": "The dollar amount to pay out. 0 if denying or escalating.",
				},
				"reasoning": map[string]any{
					"type":        "string",
					"description": "A concise explanation of the decision, referencing the tool findings.",
				},
			},
			Required: []string{"decision", "payout", "reasoning"},
		},
	}
	return anthropic.ToolUnionParam{OfTool: &def}
}

// Triage runs the tool-use loop for a single claim and returns the final
// decision. It never returns an error for an inconclusive investigation -
// those come back as an "escalate" Decision explaining why - only for a
// failed Claude request or a malformed submit_decision call.
func (a *Agent) Triage(ctx context.Context, claimText string) (Decision, error) {
	ctx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()

	toolDefs := append(a.registry.Definitions(), submitDecisionTool())

	messages := []anthropic.MessageParam{
		anthropic.NewUserMessage(anthropic.NewTextBlock(claimText)),
	}

	var steps []Step

	for i := 0; i < a.maxIterations; i++ {
		resp, err := a.client.CreateMessage(ctx, anthropic.MessageNewParams{
			Model:     a.model,
			MaxTokens: a.maxTokens,
			System:    []anthropic.TextBlockParam{{Text: systemPrompt}},
			Messages:  messages,
			Tools:     toolDefs,
		})
		if err != nil {
			return Decision{}, fmt.Errorf("agent: claude request failed on iteration %d: %w", i+1, err)
		}

		messages = append(messages, resp.ToParam())

		if resp.StopReason != anthropic.StopReasonToolUse {
			return escalate(steps, "model ended the conversation without calling submit_decision"), nil
		}

		var toolResults []anthropic.ContentBlockParamUnion

		for _, block := range resp.Content {
			tu, ok := block.AsAny().(anthropic.ToolUseBlock)
			if !ok {
				continue
			}
			raw := json.RawMessage(tu.JSON.Input.Raw())

			if tu.Name == "submit_decision" {
				var in struct {
					Decision  string  `json:"decision"`
					Payout    float64 `json:"payout"`
					Reasoning string  `json:"reasoning"`
				}
				if err := json.Unmarshal(raw, &in); err != nil {
					return Decision{}, fmt.Errorf("agent: invalid submit_decision input: %w", err)
				}
				return Decision{
					Decision:  in.Decision,
					Payout:    in.Payout,
					Reasoning: in.Reasoning,
					Steps:     steps,
				}, nil
			}

			result := a.registry.Execute(ctx, tu.Name, raw)
			steps = append(steps, Step{Tool: tu.Name, Input: raw, Output: result.Text, IsError: result.IsError})
			toolResults = append(toolResults, anthropic.NewToolResultBlock(tu.ID, result.Text, result.IsError))
		}

		if len(toolResults) == 0 {
			return escalate(steps, "model signaled tool_use but no recognized tool calls were found"), nil
		}

		messages = append(messages, anthropic.NewUserMessage(toolResults...))
	}

	return escalate(steps, fmt.Sprintf("reached max iterations (%d) without a final decision", a.maxIterations)), nil
}

func escalate(steps []Step, reason string) Decision {
	return Decision{Decision: "escalate", Payout: 0, Reasoning: reason, Steps: steps}
}
