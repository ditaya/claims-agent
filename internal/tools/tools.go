// Package tools defines the claims-agent tool set (lookup_policy,
// check_coverage, flag_fraud_signals) as plain Go functions, each paired
// with its Anthropic tool definition in a Registry the agent loop can
// execute by name.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"

	"claims-agent/internal/policy"
)

// Result is what a tool handler returns: the text to send back in the
// tool_result block, and whether it represents a failure (is_error).
type Result struct {
	Text    string
	IsError bool
}

// Handler executes one tool call given its raw JSON input.
type Handler func(ctx context.Context, input json.RawMessage) Result

// Tool pairs an Anthropic tool definition with the handler that executes it.
type Tool struct {
	Definition anthropic.ToolUnionParam
	Handler    Handler
}

// Registry is the claims-agent tool set, keyed by tool name.
type Registry struct {
	tools map[string]Tool
	order []string
}

// NewRegistry builds the standard claims-agent tool set backed by store.
func NewRegistry(store *policy.Store) *Registry {
	r := &Registry{tools: make(map[string]Tool)}
	r.add(lookupPolicyTool(store))
	r.add(checkCoverageTool(store))
	r.add(flagFraudSignalsTool(store))
	return r
}

func (r *Registry) add(t Tool) {
	name := t.Definition.OfTool.Name
	r.tools[name] = t
	r.order = append(r.order, name)
}

// Definitions returns the tool definitions in registration order, ready to
// pass as MessageNewParams.Tools.
func (r *Registry) Definitions() []anthropic.ToolUnionParam {
	defs := make([]anthropic.ToolUnionParam, 0, len(r.order))
	for _, name := range r.order {
		defs = append(defs, r.tools[name].Definition)
	}
	return defs
}

// Execute runs the named tool against input, or reports an error result if
// no such tool is registered.
func (r *Registry) Execute(ctx context.Context, name string, input json.RawMessage) Result {
	t, ok := r.tools[name]
	if !ok {
		return Result{Text: fmt.Sprintf("unknown tool %q", name), IsError: true}
	}
	return t.Handler(ctx, input)
}

// --- lookup_policy ---------------------------------------------------------

func lookupPolicyTool(store *policy.Store) Tool {
	def := anthropic.ToolParam{
		Name: "lookup_policy",
		Description: anthropic.String("Look up an insurance policy by its policy number. " +
			"Call this first whenever a claim references a policy number, to confirm it exists " +
			"and see its status, coverage type, limit, deductible, and exclusions."),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{
				"policy_number": map[string]any{
					"type":        "string",
					"description": "The policy number to look up, e.g. POL-1001",
				},
			},
			Required: []string{"policy_number"},
		},
	}

	handler := func(_ context.Context, input json.RawMessage) Result {
		var in struct {
			PolicyNumber string `json:"policy_number"`
		}
		if err := json.Unmarshal(input, &in); err != nil {
			return Result{Text: fmt.Sprintf("invalid input: %v", err), IsError: true}
		}
		p, err := store.Lookup(in.PolicyNumber)
		if err != nil {
			return Result{Text: fmt.Sprintf("no policy found with number %q", in.PolicyNumber), IsError: true}
		}
		out, err := json.Marshal(p)
		if err != nil {
			return Result{Text: fmt.Sprintf("internal error marshaling policy: %v", err), IsError: true}
		}
		return Result{Text: string(out)}
	}

	return Tool{Definition: anthropic.ToolUnionParam{OfTool: &def}, Handler: handler}
}

// --- check_coverage ----------------------------------------------------------

// CoverageAssessment is the JSON shape returned by check_coverage.
type CoverageAssessment struct {
	Covered    bool    `json:"covered"`
	Reason     string  `json:"reason"`
	MaxPayout  float64 `json:"max_payout"`
	Deductible float64 `json:"deductible"`
}

func checkCoverageTool(store *policy.Store) Tool {
	def := anthropic.ToolParam{
		Name: "check_coverage",
		Description: anthropic.String("Determine whether a specific claim type and amount is covered under a " +
			"policy, accounting for policy status, exclusions, the coverage limit/deductible, and - if an " +
			"incident date is given - whether the loss occurred while the policy was in force. Call this " +
			"after lookup_policy, once you know what kind of loss is being claimed."),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{
				"policy_number": map[string]any{
					"type":        "string",
					"description": "The policy number, e.g. POL-1001",
				},
				"claim_type": map[string]any{
					"type":        "string",
					"description": "The kind of loss being claimed, e.g. collision, theft, water damage, flood",
				},
				"claimed_amount": map[string]any{
					"type":        "number",
					"description": "The dollar amount being claimed",
				},
				"incident_date": map[string]any{
					"type":        "string",
					"description": "Date of the incident in YYYY-MM-DD format, if known. Used to verify the loss occurred within the policy's effective/expiration window.",
				},
			},
			Required: []string{"policy_number", "claim_type", "claimed_amount"},
		},
	}

	handler := func(_ context.Context, input json.RawMessage) Result {
		var in struct {
			PolicyNumber  string  `json:"policy_number"`
			ClaimType     string  `json:"claim_type"`
			ClaimedAmount float64 `json:"claimed_amount"`
			IncidentDate  string  `json:"incident_date"`
		}
		if err := json.Unmarshal(input, &in); err != nil {
			return Result{Text: fmt.Sprintf("invalid input: %v", err), IsError: true}
		}
		p, err := store.Lookup(in.PolicyNumber)
		if err != nil {
			return Result{Text: fmt.Sprintf("no policy found with number %q", in.PolicyNumber), IsError: true}
		}

		assessment := CoverageAssessment{Deductible: p.Deductible}
		outOfPeriodReason := ""
		if in.IncidentDate != "" {
			outOfPeriodReason = outsidePolicyPeriod(p, in.IncidentDate)
		}

		switch {
		case p.Status != "active":
			assessment.Reason = fmt.Sprintf("policy status is %q, not active", p.Status)
		case outOfPeriodReason != "":
			assessment.Reason = outOfPeriodReason
		case excludedPeril(p.Exclusions, in.ClaimType) != "":
			assessment.Reason = fmt.Sprintf("claim type %q matches policy exclusion %q", in.ClaimType, excludedPeril(p.Exclusions, in.ClaimType))
		default:
			payout := in.ClaimedAmount
			if payout > p.CoverageLimit {
				payout = p.CoverageLimit
			}
			payout -= p.Deductible
			if payout < 0 {
				payout = 0
			}
			assessment.Covered = true
			assessment.MaxPayout = payout
			assessment.Reason = "claim type is covered and policy is active"
		}

		out, err := json.Marshal(assessment)
		if err != nil {
			return Result{Text: fmt.Sprintf("internal error marshaling assessment: %v", err), IsError: true}
		}
		return Result{Text: string(out)}
	}

	return Tool{Definition: anthropic.ToolUnionParam{OfTool: &def}, Handler: handler}
}

// outsidePolicyPeriod returns a human-readable reason if incidentDate falls
// outside [p.EffectiveDate, p.ExpirationDate], or "" if it's in range or any
// of the dates fail to parse (in which case the period check is skipped
// rather than silently denying the claim).
func outsidePolicyPeriod(p policy.Policy, incidentDate string) string {
	incident, err := time.Parse("2006-01-02", incidentDate)
	if err != nil {
		return ""
	}
	if effective, err := time.Parse("2006-01-02", p.EffectiveDate); err == nil && incident.Before(effective) {
		return fmt.Sprintf("incident date %s is before the policy's effective date %s", incidentDate, p.EffectiveDate)
	}
	if expiration, err := time.Parse("2006-01-02", p.ExpirationDate); err == nil && incident.After(expiration) {
		return fmt.Sprintf("incident date %s is after the policy's expiration date %s", incidentDate, p.ExpirationDate)
	}
	return ""
}

// excludedPeril returns the exclusion string that claimType matches
// (case-insensitive substring match in either direction), or "" if none match.
func excludedPeril(exclusions []string, claimType string) string {
	claimType = strings.ToLower(strings.TrimSpace(claimType))
	for _, ex := range exclusions {
		exLower := strings.ToLower(ex)
		if strings.Contains(claimType, exLower) || strings.Contains(exLower, claimType) {
			return ex
		}
	}
	return ""
}

// --- flag_fraud_signals ------------------------------------------------------

// FraudAssessment is the JSON shape returned by flag_fraud_signals.
type FraudAssessment struct {
	RiskScore int      `json:"risk_score"` // 0-100, higher = more suspicious
	Signals   []string `json:"signals"`
}

// suspiciousPhrases are vague or unverifiable phrasings commonly associated
// with exaggerated or fabricated claims. This is a deliberately simple,
// deterministic heuristic - see the README's "What I'd add for production".
var suspiciousPhrases = []string{
	"no receipt", "paid cash", "no witnesses", "can't remember",
	"already disposed", "threw it away", "friend said i should claim",
	"lost the paperwork",
}

func flagFraudSignalsTool(store *policy.Store) Tool {
	def := anthropic.ToolParam{
		Name: "flag_fraud_signals",
		Description: anthropic.String("Scan a claim description (and, if known, its policy number, claimed " +
			"amount, and incident date) for heuristic fraud risk signals: vague or unverifiable language, " +
			"suspiciously round amounts, amounts exceeding the coverage limit, claims filed shortly after the " +
			"policy started, or claims against an inactive policy. Call this before making a final decision."),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{
				"claim_description": map[string]any{
					"type":        "string",
					"description": "The claimant's free-text description of the incident",
				},
				"policy_number": map[string]any{
					"type":        "string",
					"description": "The policy number, if known, e.g. POL-1001",
				},
				"claimed_amount": map[string]any{
					"type":        "number",
					"description": "The dollar amount being claimed, if known",
				},
				"incident_date": map[string]any{
					"type":        "string",
					"description": "Date of the incident in YYYY-MM-DD format, if known",
				},
			},
			Required: []string{"claim_description"},
		},
	}

	handler := func(_ context.Context, input json.RawMessage) Result {
		var in struct {
			ClaimDescription string  `json:"claim_description"`
			PolicyNumber     string  `json:"policy_number"`
			ClaimedAmount    float64 `json:"claimed_amount"`
			IncidentDate     string  `json:"incident_date"`
		}
		if err := json.Unmarshal(input, &in); err != nil {
			return Result{Text: fmt.Sprintf("invalid input: %v", err), IsError: true}
		}

		var signals []string
		score := 0

		descLower := strings.ToLower(in.ClaimDescription)
		for _, phrase := range suspiciousPhrases {
			if strings.Contains(descLower, phrase) {
				signals = append(signals, fmt.Sprintf("description contains vague/unverifiable phrase: %q", phrase))
				score += 15
			}
		}

		if in.ClaimedAmount > 0 && in.ClaimedAmount >= 1000 && int64(in.ClaimedAmount)%1000 == 0 {
			signals = append(signals, "claimed amount is a suspiciously round number")
			score += 10
		}

		if in.PolicyNumber != "" {
			if p, err := store.Lookup(in.PolicyNumber); err == nil {
				if p.Status != "active" {
					signals = append(signals, fmt.Sprintf("policy status is %q, not active", p.Status))
					score += 25
				}
				if in.ClaimedAmount > p.CoverageLimit {
					signals = append(signals, "claimed amount exceeds the policy coverage limit")
					score += 15
				}
				if in.IncidentDate != "" {
					if incident, err := time.Parse("2006-01-02", in.IncidentDate); err == nil {
						if effective, err := time.Parse("2006-01-02", p.EffectiveDate); err == nil {
							if days := incident.Sub(effective).Hours() / 24; days >= 0 && days < 14 {
								signals = append(signals, "incident occurred within 14 days of the policy's effective date")
								score += 30
							}
						}
					}
				}
			}
		}

		if score > 100 {
			score = 100
		}

		out, err := json.Marshal(FraudAssessment{RiskScore: score, Signals: signals})
		if err != nil {
			return Result{Text: fmt.Sprintf("internal error marshaling assessment: %v", err), IsError: true}
		}
		return Result{Text: string(out)}
	}

	return Tool{Definition: anthropic.ToolUnionParam{OfTool: &def}, Handler: handler}
}
