package config

import (
	"fmt"
	"strconv"
	"time"
)

// Duration wraps time.Duration so YAML can carry human readable values such as
// "20m". goccy/go-yaml decodes a bare duration into an int64 of nanoseconds,
// which is not what anyone writing a config file expects.
type Duration time.Duration

// D returns the underlying time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

func (d Duration) String() string { return time.Duration(d).String() }

// UnmarshalYAML accepts either a duration string ("20m") or a plain number of
// seconds.
func (d *Duration) UnmarshalYAML(unmarshal func(any) error) error {
	var s string
	if err := unmarshal(&s); err != nil {
		return fmt.Errorf("duration must be a string like %q or a number of seconds", "20m")
	}
	if parsed, perr := time.ParseDuration(s); perr == nil {
		*d = Duration(parsed)
		return nil
	}
	// goccy/go-yaml happily decodes a bare scalar such as `45` into a string,
	// so a unit-less value lands here rather than in a numeric branch.
	secs, ferr := strconv.ParseFloat(s, 64)
	if ferr != nil {
		return fmt.Errorf("invalid duration %q: want a string like %q or a number of seconds", s, "20m")
	}
	*d = Duration(time.Duration(secs * float64(time.Second)))
	return nil
}

// MarshalYAML renders the duration back as a human readable string.
func (d Duration) MarshalYAML() (any, error) { return time.Duration(d).String(), nil }
