package matcher

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/Immortal-Beyond-Oblivion/SettleTrace/internal/recon"
)

// RankedCandidate is one advisory-ranked settlement candidate returned by the fuzzy-ranker
// service. It carries a score, never a decision: the service's own contract
// (services/fuzzy-ranker/app.py) has no "matched" or "decision" field in its response, and
// this type mirrors that deliberately, so nothing in Go can accidentally treat a rank as a
// match.
type RankedCandidate struct {
	ID       string         `json:"id"`
	Score    float64        `json:"score"`
	Features map[string]any `json:"features"`
}

// Tier3Client calls the fuzzy-ranker service's /rank endpoint. It never writes a
// match_results row itself and has no method that could: the caller (dispatcher.go) is
// responsible for turning a successful rank into a MULTI_CANDIDATE_AMBIGUOUS exception with
// the ranked candidates as evidence, matching the "advisory-only" contract implementation.md
// and architecture.md both describe for Tier 3.
type Tier3Client struct {
	BaseURL string
	HTTP    *http.Client
}

// NewTier3Client constructs a bounded-timeout client, or nil when no fuzzy-ranker URL is
// configured. Returning nil (rather than a client that always errors) lets the caller treat
// "not configured" exactly like "temporarily unavailable": both degrade to a plain
// deterministic exception, never fail the batch.
func NewTier3Client(baseURL string) *Tier3Client {
	if baseURL == "" {
		return nil
	}
	return &Tier3Client{BaseURL: baseURL, HTTP: &http.Client{Timeout: 3 * time.Second}}
}

// rankRequest mirrors services/fuzzy-ranker/app.py's RankRequest pydantic model exactly.
type rankRequest struct {
	ExpectedNetPaise int64           `json:"expected_net_paise"`
	Candidates       []rankCandidate `json:"candidates"`
}

// rankCandidate mirrors services/fuzzy-ranker/app.py's Candidate pydantic model exactly.
type rankCandidate struct {
	ID          string `json:"id"`
	CreditPaise int64  `json:"credit_paise"`
}

// Rank asks the fuzzy-ranker service to score every already-narrowed candidate against one
// payment's expected net amount. It returns an error whenever the call could not be
// completed; it does not retry, and it does not decide what an error means for the batch,
// since that is the caller's call to make (see runSettlementSide's degrade-on-error path).
func (client *Tier3Client) Rank(ctx context.Context, payment recon.Payment, candidates []recon.SettlementLine) ([]RankedCandidate, error) {
	if client == nil {
		return nil, fmt.Errorf("tier3 client not configured")
	}
	body := rankRequest{ExpectedNetPaise: payment.NetAmount()}
	for _, candidate := range candidates {
		body.Candidates = append(body.Candidates, rankCandidate{ID: candidate.ID, CreditPaise: candidate.CreditPaise})
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal rank request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.BaseURL+"/rank", bytes.NewReader(encoded))
	if err != nil {
		return nil, fmt.Errorf("build rank request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.HTTP.Do(request)
	if err != nil {
		return nil, fmt.Errorf("call fuzzy-ranker: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fuzzy-ranker returned status %d", response.StatusCode)
	}
	var ranked []RankedCandidate
	if err := json.NewDecoder(response.Body).Decode(&ranked); err != nil {
		return nil, fmt.Errorf("decode rank response: %w", err)
	}
	return ranked, nil
}
