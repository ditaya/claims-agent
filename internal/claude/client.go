// Package claude puts the Anthropic Messages API behind a small interface so
// the agent loop can be tested against a scripted fake instead of the real
// network call.
package claude

import (
	"context"

	"github.com/anthropics/anthropic-sdk-go"
)

// Client sends one Messages API request and returns the response. The
// manual tool-use loop in internal/agent calls this once per turn.
type Client interface {
	CreateMessage(ctx context.Context, params anthropic.MessageNewParams) (*anthropic.Message, error)
}

// AnthropicClient is the production Client backed by the real Anthropic API.
// Credentials are resolved by the SDK (ANTHROPIC_API_KEY, ANTHROPIC_AUTH_TOKEN,
// or an `ant auth login` profile) - see anthropic.NewClient.
type AnthropicClient struct {
	sdk anthropic.Client
}

// New builds an AnthropicClient using the SDK's default credential resolution.
func New() *AnthropicClient {
	return &AnthropicClient{sdk: anthropic.NewClient()}
}

func (c *AnthropicClient) CreateMessage(ctx context.Context, params anthropic.MessageNewParams) (*anthropic.Message, error) {
	return c.sdk.Messages.New(ctx, params)
}
