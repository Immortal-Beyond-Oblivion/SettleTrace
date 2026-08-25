package schema

import (
	"errors"
	"regexp"
	"unicode"
)

// ErrPossiblePANDetected is returned when a payload contains a Luhn-valid digit run.
var ErrPossiblePANDetected = errors.New("possible card number detected")

var digitRun = regexp.MustCompile(`\d{13,19}`)

// RejectPossiblePAN rejects a whole payload when any 13-19 digit run passes the Luhn check.
func RejectPossiblePAN(raw []byte) error {
	filtered := stripSeparators(raw)
	for _, match := range digitRun.FindAll(filtered, -1) {
		if luhnValid(match) {
			return ErrPossiblePANDetected
		}
	}
	return nil
}

// stripSeparators removes spaces and hyphens so grouped card numbers are still detected.
func stripSeparators(raw []byte) []byte {
	out := make([]byte, 0, len(raw))
	for _, value := range raw {
		if value == ' ' || value == '-' {
			continue
		}
		if unicode.IsSpace(rune(value)) {
			continue
		}
		out = append(out, value)
	}
	return out
}

// luhnValid reports whether digits form a Luhn-valid card number.
func luhnValid(digits []byte) bool {
	sum := 0
	alternate := false
	for index := len(digits) - 1; index >= 0; index-- {
		digit := int(digits[index] - '0')
		if digit < 0 || digit > 9 {
			return false
		}
		if alternate {
			digit *= 2
			if digit > 9 {
				digit -= 9
			}
		}
		sum += digit
		alternate = !alternate
	}
	return sum%10 == 0
}
