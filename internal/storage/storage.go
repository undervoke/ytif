// Package storage shares retention policy, locks, and maintenance receipts
// across the local record pool and downloaded CI records.
package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

const SegmentBytes int64 = 8 << 20

type Policy struct {
	Days          int   `json:"days"`
	LocalMaxBytes int64 `json:"local_max_bytes"`
	CIMaxBytes    int64 `json:"ci_max_bytes"`
}

func Defaults() Policy {
	return Policy{Days: 400, LocalMaxBytes: 1 << 30, CIMaxBytes: 1 << 30}
}

func Dir(common string) string { return filepath.Join(common, "ytif") }

func Load(common string) (Policy, error) {
	p := Defaults()
	path := filepath.Join(Dir(common), "storage.json")
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	defer f.Close()
	var raw *struct {
		Days          *int   `json:"days"`
		LocalMaxBytes *int64 `json:"local_max_bytes"`
		CIMaxBytes    *int64 `json:"ci_max_bytes"`
	}
	d := json.NewDecoder(f)
	d.DisallowUnknownFields()
	if err := d.Decode(&raw); err != nil {
		return p, fmt.Errorf("%s: %w", path, err)
	}
	if raw == nil {
		return p, fmt.Errorf("%s: expected a policy object", path)
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return p, fmt.Errorf("%s: expected one policy object", path)
	}
	if raw.Days != nil {
		p.Days = *raw.Days
	}
	if raw.LocalMaxBytes != nil {
		p.LocalMaxBytes = *raw.LocalMaxBytes
	}
	if raw.CIMaxBytes != nil {
		p.CIMaxBytes = *raw.CIMaxBytes
	}
	if p.Days <= 0 || p.LocalMaxBytes <= 0 || p.CIMaxBytes <= 0 {
		return Defaults(), fmt.Errorf("%s: days and byte budgets must be positive", path)
	}
	return p, nil
}

type Boundary struct {
	Time time.Time `json:"time"`
	ID   int64     `json:"id,omitempty"`
}

func (b Boundary) Before(other Boundary) bool {
	return b.Time.Before(other.Time) || b.Time.Equal(other.Time) && b.ID < other.ID
}

type Pool struct {
	LastRun        time.Time `json:"last_run"`
	ReclaimedBytes int64     `json:"reclaimed_bytes"`
	ReclaimedUnits int       `json:"reclaimed_units"`
	ManagedBytes   int64     `json:"managed_bytes"`
	LegacyBytes    int64     `json:"legacy_bytes,omitempty"`
	Boundary       *Boundary `json:"boundary,omitempty"`
	Error          string    `json:"error,omitempty"`
	Overage        int64     `json:"overage,omitempty"`
}

type Receipt struct {
	Policy Policy          `json:"policy"`
	Local  Pool            `json:"local"`
	CI     map[string]Pool `json:"ci,omitempty"`
}

func ReadReceipt(common string) (Receipt, error) {
	var r Receipt
	b, err := os.ReadFile(filepath.Join(Dir(common), "maintenance.json"))
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return r, err
	}
	err = json.Unmarshal(b, &r)
	return r, err
}

// Each pool replaces its latest receipt while preserving its eviction boundary.
func UpdateReceipt(common, kind, key string, p Policy, pool Pool) error {
	lock, err := Lock(filepath.Join(Dir(common), "receipt.lock"), true)
	if err != nil {
		return err
	}
	defer Unlock(lock)
	r, err := ReadReceipt(common)
	if err != nil {
		return err
	}
	r.Policy = p
	if r.CI == nil {
		r.CI = map[string]Pool{}
	}
	old := r.Local
	if kind == "ci" {
		old = r.CI[key]
	}
	if old.Boundary != nil && (pool.Boundary == nil || pool.Boundary.Before(*old.Boundary)) {
		pool.Boundary = old.Boundary
	}
	if kind == "ci" {
		r.CI[key] = pool
	} else {
		r.Local = pool
	}
	return WriteJSON(filepath.Join(Dir(common), "maintenance.json"), r)
}

// WriteJSON durably publishes control state before any corresponding eviction.
func WriteJSON(path string, value any) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".retention-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(append(b, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
