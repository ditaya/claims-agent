// Command claims-agent triages insurance claims with Claude, either once
// from the CLI or continuously over HTTP.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"claims-agent/internal/agent"
	"claims-agent/internal/claude"
	"claims-agent/internal/httpapi"
	"claims-agent/internal/policy"
	"claims-agent/internal/tools"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "triage":
		err = runTriage(os.Args[2:])
	case "serve":
		err = runServe(os.Args[2:])
	case "-h", "-help", "--help", "help":
		usage()
		return
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "claims-agent:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `claims-agent - Claude-powered insurance claims triage agent

Usage:
  claims-agent triage [flags] "<claim text>"
  claims-agent serve [flags]

Run "claims-agent triage -h" or "claims-agent serve -h" for flag details.`)
}

// buildAgent wires up the policy store, tool registry, Claude client, and
// agent for either subcommand.
func buildAgent(policiesPath, model string, maxIterations int, timeout time.Duration) (*agent.Agent, error) {
	store, err := policy.Load(policiesPath)
	if err != nil {
		return nil, fmt.Errorf("loading policies: %w", err)
	}
	registry := tools.NewRegistry(store)
	client := claude.New()
	return agent.New(client, registry,
		agent.WithModel(model),
		agent.WithMaxIterations(maxIterations),
		agent.WithTimeout(timeout),
	), nil
}

func runTriage(args []string) error {
	fs := flag.NewFlagSet("triage", flag.ExitOnError)
	policiesPath := fs.String("policies", "data/policies.json", "path to the seeded policies JSON file")
	model := fs.String("model", "claude-opus-5", "Claude model ID to use")
	maxIterations := fs.Int("max-iterations", 8, "max tool-use round trips before escalating")
	timeout := fs.Duration("timeout", 60*time.Second, "overall context timeout for one triage")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return fmt.Errorf(`triage requires a claim: claims-agent triage "<claim text>"`)
	}
	claimText := strings.Join(fs.Args(), " ")

	a, err := buildAgent(*policiesPath, *model, *maxIterations, *timeout)
	if err != nil {
		return err
	}

	decision, err := a.Triage(context.Background(), claimText)
	if err != nil {
		return fmt.Errorf("triage failed: %w", err)
	}

	out, err := json.MarshalIndent(decision, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding decision: %w", err)
	}
	fmt.Println(string(out))
	return nil
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", ":8080", "address to listen on")
	policiesPath := fs.String("policies", "data/policies.json", "path to the seeded policies JSON file")
	model := fs.String("model", "claude-opus-5", "Claude model ID to use")
	maxIterations := fs.Int("max-iterations", 8, "max tool-use round trips before escalating")
	timeout := fs.Duration("timeout", 60*time.Second, "overall context timeout per triage request")
	if err := fs.Parse(args); err != nil {
		return err
	}

	a, err := buildAgent(*policiesPath, *model, *maxIterations, *timeout)
	if err != nil {
		return err
	}

	mux := httpapi.NewMux(a)
	log.Printf("claims-agent: listening on %s (POST /triage)", *addr)
	return http.ListenAndServe(*addr, mux)
}
