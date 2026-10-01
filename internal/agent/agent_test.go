package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"

	"claims-agent/internal/claude"
	"claims-agent/internal/policy"
	"claims-agent/internal/tools"
)

func testRegistry() *tools.Registry {
	store := policy.NewStore([]policy.Policy{
		{
			PolicyNumber: "POL-1001", HolderName: "Jane Doe", CoverageType: "auto",
			CoverageLimit: 25000, Deductible: 500, Status: "active",
			EffectiveDate: "2024-01-01", ExpirationDate: "2026-12-31",
			Exclusions: []string{"racing"},
		},
	})
	return tools.NewRegistry(store)
}

func TestTriageHappyPathApprove(t *testing.T) {
	fake := &claude.Fake{
		Responses: []*anthropic.Message{
			claude.ToolUseMessage(claude.ToolCall{
				ID: "tu_1", Name: "lookup_policy", Input: map[string]any{"policy_number": "POL-1001"},
			}),
			claude.ToolUseMessage(claude.ToolCall{
				ID: "tu_2", Name: "check_coverage",
				Input: map[string]any{"policy_number": "POL-1001", "claim_type": "collision", "claimed_amount": 2000},
			}),
			claude.ToolUseMessage(claude.ToolCall{
				ID: "tu_3", Name: "flag_fraud_signals",
				Input: map[string]any{"claim_description": "Rear-ended at a stop light.", "policy_number": "POL-1001"},
			}),
			claude.ToolUseMessage(claude.ToolCall{
				ID: "tu_4", Name: "submit_decision",
				Input: map[string]any{"decision": "approve", "payout": 1500.0, "reasoning": "Covered collision, active policy, no fraud signals."},
			}),
		},
	}

	a := New(fake, testRegistry(), WithMaxIterations(8), WithTimeout(5*time.Second))
	decision, err := a.Triage(context.Background(), "My car POL-1001 was rear-ended, claiming $2000.")
	if err != nil {
		t.Fatalf("Triage() error = %v", err)
	}

	if decision.Decision != "approve" {
		t.Errorf("Decision = %q, want %q", decision.Decision, "approve")
	}
	if decision.Payout != 1500.0 {
		t.Errorf("Payout = %v, want 1500", decision.Payout)
	}
	if len(decision.Steps) != 3 {
		t.Fatalf("len(Steps) = %d, want 3", len(decision.Steps))
	}
	if decision.Steps[0].Tool != "lookup_policy" || decision.Steps[1].Tool != "check_coverage" || decision.Steps[2].Tool != "flag_fraud_signals" {
		t.Errorf("Steps tools = %v, want [lookup_policy check_coverage flag_fraud_signals]", stepNames(decision.Steps))
	}
	if len(fake.Calls) != 4 {
		t.Errorf("len(fake.Calls) = %d, want 4 (one per model turn)", len(fake.Calls))
	}
}

func TestTriageMaxIterationsEscalates(t *testing.T) {
	// The fake always asks for another lookup_policy call and never calls
	// submit_decision - the loop must give up after maxIterations rather
	// than looping forever.
	var responses []*anthropic.Message
	for i := 0; i < 5; i++ {
		responses = append(responses, claude.ToolUseMessage(claude.ToolCall{
			ID: "tu", Name: "lookup_policy", Input: map[string]any{"policy_number": "POL-1001"},
		}))
	}
	fake := &claude.Fake{Responses: responses}

	a := New(fake, testRegistry(), WithMaxIterations(3), WithTimeout(5*time.Second))
	decision, err := a.Triage(context.Background(), "some claim")
	if err != nil {
		t.Fatalf("Triage() error = %v", err)
	}

	if decision.Decision != "escalate" {
		t.Errorf("Decision = %q, want %q", decision.Decision, "escalate")
	}
	if len(fake.Calls) != 3 {
		t.Errorf("len(fake.Calls) = %d, want 3 (bounded by WithMaxIterations)", len(fake.Calls))
	}
}

func TestTriageEndTurnWithoutDecisionEscalates(t *testing.T) {
	fake := &claude.Fake{
		Responses: []*anthropic.Message{
			claude.TextMessage("I'm not sure what to do with this claim."),
		},
	}

	a := New(fake, testRegistry())
	decision, err := a.Triage(context.Background(), "an ambiguous claim")
	if err != nil {
		t.Fatalf("Triage() error = %v", err)
	}
	if decision.Decision != "escalate" {
		t.Errorf("Decision = %q, want %q", decision.Decision, "escalate")
	}
}

func TestTriagePropagatesClaudeError(t *testing.T) {
	fake := &claude.Fake{
		Responses: []*anthropic.Message{nil},
		Errs:      []error{errors.New("simulated API failure")},
	}

	a := New(fake, testRegistry())
	_, err := a.Triage(context.Background(), "a claim")
	if err == nil {
		t.Fatal("Triage() expected error, got nil")
	}
}

func stepNames(steps []Step) []string {
	names := make([]string, len(steps))
	for i, s := range steps {
		names[i] = s.Tool
	}
	return names
}
