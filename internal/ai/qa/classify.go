// Package qa implements the Settlement Q&A agent described in implementation.md sections 2.4
// and 8: a "template-and-retrieve" agent, not an "LLM with SQL tool access." A question is
// routed by plain rules to one of a fixed set of reviewed store queries (store.QAStore), the
// rows come back, and only then may an LLM phrase them as a sentence -- constrained to those
// rows, degrading to a deterministic summary if the LLM is unavailable. No code path in this
// package builds SQL from free text, and no path lets the model write anything.
package qa

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Intent is the closed set of question shapes the agent can answer. Adding one means adding a
// method to store.QAStore and a case in Agent.Answer -- a code-review event, never something a
// question's wording can trigger on its own.
type Intent string

const (
	// IntentWhyNotSettled answers "why didn't pay_X settle" from that payment's exception rows.
	IntentWhyNotSettled Intent = "why_not_settled"
	// IntentAmountAtRisk answers "how much/how many" from current unresolved exceptions.
	IntentAmountAtRisk Intent = "amount_at_risk"
	// IntentWorstMethod answers "which payment method matches worst" over a recent window.
	IntentWorstMethod Intent = "worst_method_by_match_rate"
	// IntentUnsupported is returned when no rule matches. It never reaches the store or the LLM.
	IntentUnsupported Intent = "unsupported"
)

// Classification is the result of routing one question: which fixed query to run, plus the
// only parameters extracted from the question's text. Parameters are extracted by narrow
// regexes (a payment ID shape, a day count), never by an LLM.
type Classification struct {
	Intent     Intent
	PaymentID  string    // set for IntentWhyNotSettled
	WindowDays int       // set for IntentWorstMethod
	Since      time.Time // set for IntentWorstMethod: now minus WindowDays, UTC
}

const (
	defaultWindowDays = 7
	maxWindowDays     = 90
)

var (
	// paymentIDPattern matches this repo's payment ID shape (pay_ followed by alphanumerics),
	// the same shape implementation.md section 8's extractPaymentID describes: nothing fancier.
	paymentIDPattern = regexp.MustCompile(`\bpay_[A-Za-z0-9]+`)
	lastNDaysPattern = regexp.MustCompile(`\blast\s+(\d{1,3})\s+days?\b`)
)

// Classify routes a question to an Intent using fixed, ordered rules. Order matters and is
// deliberate: a payment ID is the most specific signal and wins over everything else; "worst
// <something about methods/matching>" is checked before the generic "how much/how many"
// aggregate so "which method has the worst match rate" is never mistaken for an amount query.
// now is injected so window parsing is deterministic in tests.
func Classify(question string, now time.Time) Classification {
	lower := strings.ToLower(question)

	if id := paymentIDPattern.FindString(question); id != "" {
		return Classification{Intent: IntentWhyNotSettled, PaymentID: id}
	}

	if containsAny(lower, "worst", "lowest", "poorest") &&
		containsAny(lower, "method", "match rate", "match-rate", "matching") {
		days := parseWindowDays(lower)
		return Classification{
			Intent:     IntentWorstMethod,
			WindowDays: days,
			Since:      now.UTC().AddDate(0, 0, -days),
		}
	}

	if containsAny(lower, "how much", "how many", "total", "at risk", "unresolved", "outstanding") {
		return Classification{Intent: IntentAmountAtRisk}
	}

	return Classification{Intent: IntentUnsupported}
}

// parseWindowDays reads a lookback window out of a lower-cased question. An explicit "last N
// days" wins, then "today", then month/week phrasing, then the 7-day default. The result is
// always clamped to [1, maxWindowDays] so a question cannot ask for an unbounded scan.
func parseWindowDays(lower string) int {
	if match := lastNDaysPattern.FindStringSubmatch(lower); match != nil {
		if n, err := strconv.Atoi(match[1]); err == nil {
			return clampDays(n)
		}
	}
	switch {
	case strings.Contains(lower, "today"):
		return 1
	case strings.Contains(lower, "month"):
		return 30
	default:
		// "this week", "last week", or no window phrase at all.
		return defaultWindowDays
	}
}

func clampDays(days int) int {
	if days < 1 {
		return 1
	}
	if days > maxWindowDays {
		return maxWindowDays
	}
	return days
}

func containsAny(text string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}
