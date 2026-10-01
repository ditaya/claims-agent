package claude

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
)

// Fake is a scriptable Client for tests: each call to CreateMessage returns
// the next entry in Responses, in order. It is exported (not a _test.go
// file) so other packages' tests - internal/agent, internal/httpapi - can
// use it too.
type Fake struct {
	Responses []*anthropic.Message
	Errs      []error // optional: parallel slice, Errs[i] returned instead of Responses[i] if non-nil

	// Calls records every request passed to CreateMessage, for assertions
	// about what the agent sent each turn.
	Calls []anthropic.MessageNewParams

	next int
}

func (f *Fake) CreateMessage(_ context.Context, params anthropic.MessageNewParams) (*anthropic.Message, error) {
	f.Calls = append(f.Calls, params)

	if f.next >= len(f.Responses) {
		return nil, fmt.Errorf("fake claude client: no scripted response for call %d", f.next+1)
	}
	i := f.next
	f.next++

	if i < len(f.Errs) && f.Errs[i] != nil {
		return nil, f.Errs[i]
	}
	return f.Responses[i], nil
}

// TextMessage builds a *anthropic.Message whose content is a single text
// block, with stop_reason "end_turn" - a convenience for scripting a final,
// non-tool-use turn in tests.
//
// This goes through a JSON round trip rather than a struct literal: the
// union types in this SDK (ContentBlockUnion, etc.) cache the raw JSON they
// were parsed from and use it to implement AsAny()/AsToolUse(); a struct
// literal never populates that cache, so AsAny() silently returns a
// zero-value variant. Building the message as plain JSON and unmarshaling
// it - exactly like a real HTTP response - keeps the fake honest.
func TextMessage(text string) *anthropic.Message {
	return mustUnmarshalMessage(map[string]any{
		"id":          "msg_fake",
		"type":        "message",
		"role":        "assistant",
		"model":       "claude-opus-5",
		"stop_reason": "end_turn",
		"content": []map[string]any{
			{"type": "text", "text": text},
		},
		"usage": map[string]any{"input_tokens": 0, "output_tokens": 0},
	})
}

// ToolUseMessage builds a *anthropic.Message whose content is one or more
// tool_use blocks, with stop_reason "tool_use" - a convenience for scripting
// a (possibly parallel) tool call in tests. See TextMessage for why this
// goes through JSON instead of a struct literal.
func ToolUseMessage(calls ...ToolCall) *anthropic.Message {
	content := make([]map[string]any, 0, len(calls))
	for _, c := range calls {
		content = append(content, map[string]any{
			"type":  "tool_use",
			"id":    c.ID,
			"name":  c.Name,
			"input": c.Input,
		})
	}
	return mustUnmarshalMessage(map[string]any{
		"id":          "msg_fake",
		"type":        "message",
		"role":        "assistant",
		"model":       "claude-opus-5",
		"stop_reason": "tool_use",
		"content":     content,
		"usage":       map[string]any{"input_tokens": 0, "output_tokens": 0},
	})
}

func mustUnmarshalMessage(v map[string]any) *anthropic.Message {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("claude: marshal fake message: %v", err))
	}
	var msg anthropic.Message
	if err := json.Unmarshal(raw, &msg); err != nil {
		panic(fmt.Sprintf("claude: unmarshal fake message: %v", err))
	}
	return &msg
}

// ToolCall is one scripted tool_use block for ToolUseMessage.
type ToolCall struct {
	ID    string
	Name  string
	Input map[string]any
}
