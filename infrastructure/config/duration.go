package config

import (
	"encoding/json"
	"fmt"
	"time"
)

// Duration is a time.Duration whose JSON form is the human string —
// "30s", "5m" — with a bare number accepted as raw nanoseconds.
// time.Duration itself would only unmarshal the number, which nobody
// wants to write in a config file.
type Duration time.Duration

// MarshalJSON emits the human string — the symmetric form of
// UnmarshalJSON, so a generated config stays editable.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

func (d *Duration) UnmarshalJSON(raw []byte) error {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return err
	}
	switch x := v.(type) {
	case string:
		parsed, err := time.ParseDuration(x)
		if err != nil {
			return err
		}
		*d = Duration(parsed)
	case float64:
		*d = Duration(int64(x))
	default:
		return fmt.Errorf("duration must be a string or nanoseconds, got %T", v)
	}
	return nil
}
