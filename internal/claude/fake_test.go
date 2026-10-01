package claude

import (
	"context"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
)

func TestFakeReturnsScriptedResponsesInOrder(t *testing.T) {
	fake := &Fake{
		Responses: []*anthropic.Message{
			ToolUseMessage(ToolCall{ID: "tu_1", Name: "lookup_policy", Input: map[string]any{"policy_number": "POL-1001"}}),
			TextMessage("done"),
		},
	}

	msg1, err := fake.CreateMessage(context.Background(), anthropic.MessageNewParams{})
	if err != nil {
		t.Fatalf("call 1: unexpected error: %v", err)
	}
	if msg1.StopReason != anthropic.StopReasonToolUse {
		t.Errorf("call 1: StopReason = %q, want %q", msg1.StopReason, anthropic.StopReasonToolUse)
	}

	msg2, err := fake.CreateMessage(context.Background(), anthropic.MessageNewParams{})
	if err != nil {
		t.Fatalf("call 2: unexpected error: %v", err)
	}
	if msg2.StopReason != anthropic.StopReasonEndTurn {
		t.Errorf("call 2: StopReason = %q, want %q", msg2.StopReason, anthropic.StopReasonEndTurn)
	}

	if len(fake.Calls) != 2 {
		t.Errorf("len(Calls) = %d, want 2", len(fake.Calls))
	}
}

func TestFakeExhaustedScriptReturnsError(t *testing.T) {
	fake := &Fake{Responses: []*anthropic.Message{TextMessage("only one")}}

	if _, err := fake.CreateMessage(context.Background(), anthropic.MessageNewParams{}); err != nil {
		t.Fatalf("call 1: unexpected error: %v", err)
	}
	if _, err := fake.CreateMessage(context.Background(), anthropic.MessageNewParams{}); err == nil {
		t.Error("call 2: expected error when script is exhausted, got nil")
	}
}
