package recon

import "time"

// Clock supplies UTC timestamps so matching windows do not depend on host time zones.
type Clock interface {
	Now() time.Time
}

// UTCClock returns the current time in UTC.
type UTCClock struct{}

// Now returns the current UTC timestamp.
func (UTCClock) Now() time.Time { return time.Now().UTC() }
