package tools

import (
	"context"
	"encoding/json"
	"testing"

	"claims-agent/internal/policy"
)

func testStore() *policy.Store {
	return policy.NewStore([]policy.Policy{
		{
			PolicyNumber: "POL-1001", HolderName: "Jane Doe", CoverageType: "auto",
			CoverageLimit: 25000, Deductible: 500, Status: "active",
			EffectiveDate: "2024-01-01", ExpirationDate: "2026-12-31",
			Exclusions: []string{"racing", "commercial use"},
		},
		{
			PolicyNumber: "POL-1003", HolderName: "Priya Nair", CoverageType: "auto",
			CoverageLimit: 20000, Deductible: 500, Status: "lapsed",
			EffectiveDate: "2022-01-01", ExpirationDate: "2023-01-01",
		},
		{
			PolicyNumber: "POL-1006", HolderName: "Oliver Smith", CoverageType: "auto",
			CoverageLimit: 10000, Deductible: 1000, Status: "active",
			EffectiveDate: "2026-09-20", ExpirationDate: "2027-09-20",
			Exclusions: []string{"racing"},
		},
	})
}

func TestLookupPolicyTool(t *testing.T) {
	reg := NewRegistry(testStore())

	res := reg.Execute(context.Background(), "lookup_policy", json.RawMessage(`{"policy_number":"POL-1001"}`))
	if res.IsError {
		t.Fatalf("unexpected error result: %s", res.Text)
	}
	var p policy.Policy
	if err := json.Unmarshal([]byte(res.Text), &p); err != nil {
		t.Fatalf("result not valid JSON: %v", err)
	}
	if p.HolderName != "Jane Doe" {
		t.Errorf("HolderName = %q, want %q", p.HolderName, "Jane Doe")
	}

	res = reg.Execute(context.Background(), "lookup_policy", json.RawMessage(`{"policy_number":"POL-9999"}`))
	if !res.IsError {
		t.Error("expected is_error for unknown policy, got success")
	}
}

func TestCheckCoverageTool(t *testing.T) {
	reg := NewRegistry(testStore())

	tests := []struct {
		name          string
		input         string
		wantCovered   bool
		wantMaxPayout float64
	}{
		{
			name:          "covered claim within limit",
			input:         `{"policy_number":"POL-1001","claim_type":"collision","claimed_amount":5000}`,
			wantCovered:   true,
			wantMaxPayout: 4500, // 5000 - 500 deductible
		},
		{
			name:          "claim exceeds coverage limit",
			input:         `{"policy_number":"POL-1001","claim_type":"collision","claimed_amount":100000}`,
			wantCovered:   true,
			wantMaxPayout: 24500, // capped at 25000 limit, minus 500 deductible
		},
		{
			name:        "excluded peril denied",
			input:       `{"policy_number":"POL-1001","claim_type":"racing","claimed_amount":5000}`,
			wantCovered: false,
		},
		{
			name:        "lapsed policy denied",
			input:       `{"policy_number":"POL-1003","claim_type":"collision","claimed_amount":5000}`,
			wantCovered: false,
		},
		{
			name:        "incident before effective date denied",
			input:       `{"policy_number":"POL-1001","claim_type":"collision","claimed_amount":5000,"incident_date":"2023-06-01"}`,
			wantCovered: false,
		},
		{
			name:        "incident after expiration date denied",
			input:       `{"policy_number":"POL-1001","claim_type":"collision","claimed_amount":5000,"incident_date":"2027-01-15"}`,
			wantCovered: false,
		},
		{
			name:          "incident within policy period covered",
			input:         `{"policy_number":"POL-1001","claim_type":"collision","claimed_amount":5000,"incident_date":"2025-06-01"}`,
			wantCovered:   true,
			wantMaxPayout: 4500,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := reg.Execute(context.Background(), "check_coverage", json.RawMessage(tt.input))
			if res.IsError {
				t.Fatalf("unexpected error result: %s", res.Text)
			}
			var got CoverageAssessment
			if err := json.Unmarshal([]byte(res.Text), &got); err != nil {
				t.Fatalf("result not valid JSON: %v", err)
			}
			if got.Covered != tt.wantCovered {
				t.Errorf("Covered = %v, want %v (reason: %s)", got.Covered, tt.wantCovered, got.Reason)
			}
			if tt.wantCovered && got.MaxPayout != tt.wantMaxPayout {
				t.Errorf("MaxPayout = %v, want %v", got.MaxPayout, tt.wantMaxPayout)
			}
		})
	}
}

func TestFlagFraudSignalsTool(t *testing.T) {
	reg := NewRegistry(testStore())

	t.Run("clean claim has no signals", func(t *testing.T) {
		res := reg.Execute(context.Background(), "flag_fraud_signals", json.RawMessage(
			`{"claim_description":"My car was rear-ended at a red light and the bumper needs replacing.","policy_number":"POL-1001","claimed_amount":1234,"incident_date":"2026-09-01"}`))
		var got FraudAssessment
		if err := json.Unmarshal([]byte(res.Text), &got); err != nil {
			t.Fatalf("result not valid JSON: %v", err)
		}
		if got.RiskScore != 0 {
			t.Errorf("RiskScore = %d, want 0 (signals: %v)", got.RiskScore, got.Signals)
		}
	})

	t.Run("early claim on brand new policy is flagged", func(t *testing.T) {
		res := reg.Execute(context.Background(), "flag_fraud_signals", json.RawMessage(
			`{"claim_description":"Hit a deer on the highway.","policy_number":"POL-1006","claimed_amount":3000,"incident_date":"2026-09-25"}`))
		var got FraudAssessment
		if err := json.Unmarshal([]byte(res.Text), &got); err != nil {
			t.Fatalf("result not valid JSON: %v", err)
		}
		if got.RiskScore == 0 {
			t.Error("expected a nonzero risk score for an early claim, got 0")
		}
	})

	t.Run("vague language and round amount are flagged", func(t *testing.T) {
		res := reg.Execute(context.Background(), "flag_fraud_signals", json.RawMessage(
			`{"claim_description":"Paid cash for repairs, no receipt, no witnesses.","claimed_amount":5000}`))
		var got FraudAssessment
		if err := json.Unmarshal([]byte(res.Text), &got); err != nil {
			t.Fatalf("result not valid JSON: %v", err)
		}
		if len(got.Signals) < 2 {
			t.Errorf("Signals = %v, want at least 2", got.Signals)
		}
	})
}

func TestExecuteUnknownTool(t *testing.T) {
	reg := NewRegistry(testStore())
	res := reg.Execute(context.Background(), "not_a_real_tool", json.RawMessage(`{}`))
	if !res.IsError {
		t.Error("expected is_error for unknown tool, got success")
	}
}

func TestDefinitionsIncludeAllThreeTools(t *testing.T) {
	reg := NewRegistry(testStore())
	defs := reg.Definitions()
	if len(defs) != 3 {
		t.Fatalf("len(Definitions()) = %d, want 3", len(defs))
	}
	names := map[string]bool{}
	for _, d := range defs {
		if d.OfTool == nil {
			t.Fatal("tool union missing OfTool")
		}
		names[d.OfTool.Name] = true
	}
	for _, want := range []string{"lookup_policy", "check_coverage", "flag_fraud_signals"} {
		if !names[want] {
			t.Errorf("Definitions() missing tool %q", want)
		}
	}
}
