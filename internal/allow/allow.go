// Package allow keeps the user's time-limited permission for agents to run
// checks themselves, for unattended work that values accuracy over
// gate cost. The guard reads it; only the user sets it.
package allow

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

type grant struct {
	Until time.Time `json:"until"`
}

func file(commonDir string) string {
	return filepath.Join(commonDir, "ytif", "allow.json")
}

// Set allows checks until the given time.
func Set(commonDir string, until time.Time) error {
	data, err := json.Marshal(grant{Until: until.UTC()})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(file(commonDir)), 0o755); err != nil {
		return err
	}
	return os.WriteFile(file(commonDir), data, 0o644)
}

// Clear withdraws the permission.
func Clear(commonDir string) error {
	if err := os.Remove(file(commonDir)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// Until returns when the permission ends and whether it is in force at now.
// A missing or unreadable permission is not in force.
func Until(commonDir string, now time.Time) (time.Time, bool) {
	data, err := os.ReadFile(file(commonDir))
	if err != nil {
		return time.Time{}, false
	}
	var g grant
	if json.Unmarshal(data, &g) != nil {
		return time.Time{}, false
	}
	return g.Until, now.Before(g.Until)
}
