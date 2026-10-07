// Package check defines the types shared by sources, reconciliation, and gates.
package check

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"
)

// Key identifies a registered check by the runner that owns it, the unit that
// contains it, and its name path within that unit.
type Key struct {
	Runner string
	Unit   string
	Name   string
}

// String renders runner:unit:name.
func (k Key) String() string { return k.Runner + ":" + k.Unit + ":" + k.Name }

// Less orders keys by runner, unit, then name.
func (k Key) Less(o Key) bool {
	if k.Runner != o.Runner {
		return k.Runner < o.Runner
	}
	if k.Unit != o.Unit {
		return k.Unit < o.Unit
	}
	return k.Name < o.Name
}

// ParseKey reads runner:unit:name. Only the first two colons separate fields,
// so a name may contain colons.
func ParseKey(s string) (Key, error) {
	runner, rest, _ := strings.Cut(s, ":")
	unit, name, _ := strings.Cut(rest, ":")
	if runner == "" || unit == "" || name == "" {
		return Key{}, fmt.Errorf("key %q: want runner:unit:name", s)
	}
	return Key{Runner: runner, Unit: unit, Name: name}, nil
}

// Gate names.
const (
	GateCommit = "commit"
	GatePush   = "push"
	GateCI     = "ci"
)

// Rank orders placements cumulatively: a gate runs every check placed at its
// rank or earlier. It returns 0 for names that are not placements.
func Rank(name string) int {
	switch name {
	case GateCommit:
		return 1
	case GatePush:
		return 2
	case GateCI:
		return 3
	}
	return 0
}

// Outcome is the result of one executed check.
type Outcome string

const (
	Pass Outcome = "pass"
	Fail Outcome = "fail"
	// Cached is a reused successful result, with no fresh check duration.
	Cached Outcome = "cached"
	// Blocked means a dependency failed in the same invocation; it is neither
	// a pass nor a hit.
	Blocked Outcome = "blocked"
)

// Scope names the file set a gate binds to its checks.
const (
	ScopeStaged = "staged"
	ScopePushed = "pushed"
	ScopeTree   = "tree"
)

// Input binds a gate to the files its checks examine. Files never falls back
// to the whole tree: an empty list means nothing is in scope.
type Input struct {
	Gate  string
	Scope string
	Files []string // slash-separated, relative to the repository root
}

// Result is one check outcome.
type Result struct {
	Key     Key
	Outcome Outcome
	Elapsed time.Duration
	Timed   bool // false when the source cannot attribute elapsed time to this check
	Output  string
	Ended   time.Time // when the check finished, if the source knows
}

// Invocation is one build or process a source started, with its wall time.
type Invocation struct {
	Runner  string
	Unit    string
	What    string // build | run | list
	Elapsed time.Duration
	// Err reports a failure the check outcomes do not explain: a crash, a
	// build failure, or a failure outside any check. It fails the gate and
	// explains the served checks left without an outcome.
	Err  error
	Keys []Key // the checks the invocation ran
	// Interrupted reports that cancelling the run stopped the process or
	// kept it from starting.
	Interrupted bool
}

// Report is everything one Run produced. A selected key that appears in
// neither Results nor Skipped has no outcome; the gate treats it as a
// dispatch failure rather than inventing one.
type Report struct {
	Results     []Result
	Skipped     []Key // ran and reported a skip, so no outcome is recorded
	Invocations []Invocation
}

// Repo is the repository a source works in.
type Repo struct {
	Root    string    // absolute repository root
	Tracked []string  // tracked files present on disk, slash-separated
	Scratch string    // build outputs and generated files; the caller removes it
	Log     io.Writer // progress and passthrough output
}

// CoRunner is implemented by sources in which running one check
// necessarily runs others, such as a Go check's providers or tests a name
// pattern cannot separate. Reconciliation requires those to be placed at
// the same gate or earlier.
type CoRunner interface {
	// CoRuns maps a discovered key to the other keys that run with it.
	CoRuns() map[Key][]Key
}

// Source enumerates and runs the checks of one or more runners.
type Source interface {
	// Runners lists the runner names whose keys this source owns.
	Runners() []string
	// Discover returns the registry and any processes it started. A non-nil
	// error means discovery is incomplete: the keys returned exist, but a
	// missing key is no evidence that an inventory entry is stale.
	Discover(ctx context.Context, repo Repo) ([]Key, []Invocation, error)
	// Run executes keys, all owned by this source, against in.
	Run(ctx context.Context, repo Repo, keys []Key, in Input) (Report, error)
}
