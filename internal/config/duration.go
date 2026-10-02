package config

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Duration is a time.Duration that reads and writes as a human string, so the
// config file says "2h" rather than 7200000000000.
type Duration time.Duration

// String implements fmt.Stringer.
func (d Duration) String() string { return FormatTTL(time.Duration(d)) }

// MarshalJSON implements json.Marshaler.
func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(d.String()) }

// UnmarshalJSON implements json.Unmarshaler, accepting either a string such as
// "30m" or a number of seconds.
func (d *Duration) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		parsed, err := ParseTTL(text)
		if err != nil {
			return err
		}
		*d = Duration(parsed)
		return nil
	}

	var seconds float64
	if err := json.Unmarshal(data, &seconds); err != nil {
		return fmt.Errorf("config: %s is not a duration", data)
	}
	*d = Duration(time.Duration(seconds * float64(time.Second)))
	return nil
}

// ParseTTL parses a share lifetime.
//
// It accepts everything time.ParseDuration does, plus the day and week units
// that it does not, because "1d" is the natural way to write a one-day share.
// "0" and "never" mean "no expiry".
func ParseTTL(text string) (time.Duration, error) {
	text = strings.TrimSpace(strings.ToLower(text))
	switch text {
	case "", "0", "never", "none", "off":
		return 0, nil
	}

	// Days and weeks are expanded to hours before handing the rest to the
	// standard parser, which only knows up to hours.
	for suffix, unit := range map[string]time.Duration{
		"w": 7 * 24 * time.Hour,
		"d": 24 * time.Hour,
	} {
		if !strings.HasSuffix(text, suffix) {
			continue
		}
		count, err := strconv.ParseFloat(strings.TrimSuffix(text, suffix), 64)
		if err != nil {
			return 0, fmt.Errorf("config: %q is not a valid duration", text)
		}
		if count < 0 {
			return 0, fmt.Errorf("config: duration %q must not be negative", text)
		}
		return time.Duration(count * float64(unit)), nil
	}

	parsed, err := time.ParseDuration(text)
	if err != nil {
		return 0, fmt.Errorf("config: %q is not a valid duration (try 30m, 2h or 1d)", text)
	}
	if parsed < 0 {
		return 0, fmt.Errorf("config: duration %q must not be negative", text)
	}
	return parsed, nil
}

// FormatTTL renders a duration the way ParseTTL accepts it.
func FormatTTL(d time.Duration) string {
	if d <= 0 {
		return "never"
	}
	switch {
	case d%(24*time.Hour) == 0:
		return strconv.FormatInt(int64(d/(24*time.Hour)), 10) + "d"
	case d%time.Hour == 0:
		return strconv.FormatInt(int64(d/time.Hour), 10) + "h"
	case d%time.Minute == 0:
		return strconv.FormatInt(int64(d/time.Minute), 10) + "m"
	default:
		return d.String()
	}
}
