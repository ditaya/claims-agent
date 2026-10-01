package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"claims-agent/internal/agent"
)

type fakeTriager func(ctx context.Context, claim string) (agent.Decision, error)

func (f fakeTriager) Triage(ctx context.Context, claim string) (agent.Decision, error) {
	return f(ctx, claim)
}

func postTriage(t *testing.T, mux *http.ServeMux, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/triage", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestTriageEndpointSuccess(t *testing.T) {
	want := agent.Decision{Decision: "approve", Payout: 1200, Reasoning: "covered"}
	triager := fakeTriager(func(_ context.Context, claim string) (agent.Decision, error) {
		if claim != "my claim text" {
			t.Errorf("claim passed to agent = %q, want %q", claim, "my claim text")
		}
		return want, nil
	})

	rec := postTriage(t, NewMux(triager), `{"claim":"my claim text"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	var got agent.Decision
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("response not valid JSON: %v", err)
	}
	if got.Decision != want.Decision || got.Payout != want.Payout || got.Reasoning != want.Reasoning {
		t.Errorf("decision = %+v, want %+v", got, want)
	}
}

func TestTriageEndpointEmptyClaim(t *testing.T) {
	triager := fakeTriager(func(context.Context, string) (agent.Decision, error) {
		t.Fatal("triager should not be called for an empty claim")
		return agent.Decision{}, nil
	})

	rec := postTriage(t, NewMux(triager), `{"claim":""}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestTriageEndpointInvalidJSON(t *testing.T) {
	triager := fakeTriager(func(context.Context, string) (agent.Decision, error) {
		t.Fatal("triager should not be called for invalid JSON")
		return agent.Decision{}, nil
	})

	rec := postTriage(t, NewMux(triager), `not json`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestTriageEndpointAgentError(t *testing.T) {
	triager := fakeTriager(func(context.Context, string) (agent.Decision, error) {
		return agent.Decision{}, errors.New("boom")
	})

	rec := postTriage(t, NewMux(triager), `{"claim":"x"}`)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

func TestTriageEndpointWrongMethod(t *testing.T) {
	triager := fakeTriager(func(context.Context, string) (agent.Decision, error) {
		t.Fatal("triager should not be called for a GET request")
		return agent.Decision{}, nil
	})

	req := httptest.NewRequest(http.MethodGet, "/triage", nil)
	rec := httptest.NewRecorder()
	NewMux(triager).ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}
