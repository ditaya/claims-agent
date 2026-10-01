// Command runner runs the sample claims in evals/claims.json through the
// real triage agent (a live Claude API call per claim - this costs money
// and is not run as part of `go test`) and prints a pass/fail table.
//
// Usage (from the repo root, with ANTHROPIC_API_KEY set):
//
//	go run ./evals/runner
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"claims-agent/internal/agent"
	"claims-agent/internal/claude"
	"claims-agent/internal/policy"
	"claims-agent/internal/tools"
)

type claimCase struct {
	ID               string `json:"id"`
	ClaimText        string `json:"claim_text"`
	ExpectedDecision string `json:"expected_decision"`
	Notes            string `json:"notes"`
}

func main() {
	policiesPath := flag.String("policies", "data/policies.json", "path to the seeded policies JSON file")
	claimsPath := flag.String("claims", "evals/claims.json", "path to the eval claims JSON file")
	model := flag.String("model", "claude-opus-5", "Claude model ID to use")
	maxIterations := flag.Int("max-iterations", 8, "max tool-use round trips before escalating")
	timeout := flag.Duration("timeout", 60*time.Second, "per-claim context timeout")
	flag.Parse()

	store, err := policy.Load(*policiesPath)
	if err != nil {
		fatal(err)
	}
	cases, err := loadCases(*claimsPath)
	if err != nil {
		fatal(err)
	}

	registry := tools.NewRegistry(store)
	a := agent.New(claude.New(), registry,
		agent.WithModel(*model),
		agent.WithMaxIterations(*maxIterations),
		agent.WithTimeout(*timeout),
	)

	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tEXPECTED\tGOT\tMATCH\tPAYOUT\tLATENCY")

	passed := 0
	for _, c := range cases {
		start := time.Now()
		decision, err := a.Triage(context.Background(), c.ClaimText)
		elapsed := time.Since(start).Round(time.Millisecond)

		if err != nil {
			fmt.Fprintf(tw, "%s\t%s\tERROR\tFAIL\t-\t%s\n", c.ID, c.ExpectedDecision, elapsed)
			fmt.Fprintf(os.Stderr, "%s: triage error: %v\n", c.ID, err)
			continue
		}

		match := decision.Decision == c.ExpectedDecision
		if match {
			passed++
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%.2f\t%s\n",
			c.ID, c.ExpectedDecision, decision.Decision, matchLabel(match), decision.Payout, elapsed)
	}
	tw.Flush()

	fmt.Printf("\n%d/%d passed\n", passed, len(cases))
	if passed != len(cases) {
		os.Exit(1)
	}
}

func matchLabel(ok bool) string {
	if ok {
		return "PASS"
	}
	return "FAIL"
}

func loadCases(path string) ([]claimCase, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var cases []claimCase
	if err := json.Unmarshal(data, &cases); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	return cases, nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "evals:", err)
	os.Exit(1)
}
