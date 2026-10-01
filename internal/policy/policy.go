// Package policy loads and queries the seeded insurance policy book.
package policy

import (
	"encoding/json"
	"fmt"
	"os"
)

// Policy is a single insurance policy record.
type Policy struct {
	PolicyNumber   string   `json:"policy_number"`
	HolderName     string   `json:"holder_name"`
	CoverageType   string   `json:"coverage_type"` // auto, home, renters, health
	CoverageLimit  float64  `json:"coverage_limit"`
	Deductible     float64  `json:"deductible"`
	Status         string   `json:"status"`          // active, lapsed, cancelled
	EffectiveDate  string   `json:"effective_date"`  // YYYY-MM-DD
	ExpirationDate string   `json:"expiration_date"` // YYYY-MM-DD
	Exclusions     []string `json:"exclusions"`
}

// Store is an in-memory, read-only policy book keyed by policy number.
type Store struct {
	policies map[string]Policy
}

// Load reads a JSON array of Policy records from path and indexes them by
// PolicyNumber.
func Load(path string) (*Store, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("policy: read %s: %w", path, err)
	}
	var list []Policy
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("policy: parse %s: %w", path, err)
	}
	return NewStore(list), nil
}

// NewStore builds a Store directly from a slice of policies (useful in tests).
func NewStore(policies []Policy) *Store {
	s := &Store{policies: make(map[string]Policy, len(policies))}
	for _, p := range policies {
		s.policies[p.PolicyNumber] = p
	}
	return s
}

// ErrNotFound is returned by Lookup when no policy matches the given number.
var ErrNotFound = fmt.Errorf("policy not found")

// Lookup returns the policy with the given number, or ErrNotFound.
func (s *Store) Lookup(policyNumber string) (Policy, error) {
	p, ok := s.policies[policyNumber]
	if !ok {
		return Policy{}, ErrNotFound
	}
	return p, nil
}

// Len returns the number of policies in the store.
func (s *Store) Len() int {
	return len(s.policies)
}
