package gate

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/undervoke/ytif/internal/check"
)

const noOutcome = "no outcome reported"

// outcome collects what the sources reported for one run.
type outcome struct {
	reports  []check.Report
	dispatch []dispatchError // selected checks left without an outcome
	problems []string        // failures no check outcome explains
}

// collect runs each source on its selected keys. A gate holds back a key
// whose run would also run a check the gate does not select, since that
// check's placement keeps it from this gate. A source's report is kept even
// when it also returns an error; only reported outcomes for keys the run may
// produce — the selected keys and the checks that necessarily run with them
// — are accepted, each at most once and with a known outcome. Once the run
// is interrupted, no further source starts.
func collect(ctx context.Context, s *Survey, selected []check.Key, in check.Input) outcome {
	var out outcome
	co := coRuns(s)
	selected, out.dispatch = heldBack(selected, co)
	allowed := allowedKeys(selected, co)
	for _, src := range s.Sources {
		if ctx.Err() != nil {
			break
		}
		keys := ownedBy(s, src, selected)
		if len(keys) == 0 {
			continue
		}
		name := strings.Join(src.Runners(), ", ")
		rep, err := src.Run(ctx, s.Repo, keys, in)
		rep, rejected, problems := validate(rep, name, src, s, keys, allowed)
		out.problems = append(out.problems, problems...)
		out.reports = append(out.reports, rep)

		unresolved := missing(keys, rep, rejected)
		if err != nil {
			explained := false
			for i := range unresolved {
				if unresolved[i].detail == noOutcome {
					unresolved[i].detail = err.Error()
					explained = true
				}
			}
			if !explained {
				out.problems = append(out.problems, fmt.Sprintf("%s: %v", name, err))
			}
		}
		out.dispatch = append(out.dispatch, unresolved...)
	}
	return out
}

// coRuns merges every source's map of the checks that necessarily run with
// a check.
func coRuns(s *Survey) map[check.Key][]check.Key {
	co := map[check.Key][]check.Key{}
	for _, src := range s.Sources {
		if c, ok := src.(check.CoRunner); ok {
			for k, others := range c.CoRuns() {
				co[k] = append(co[k], others...)
			}
		}
	}
	return co
}

// heldBack removes the selected keys whose run would also run an
// unselected check, and reports each as left without an outcome.
func heldBack(selected []check.Key, co map[check.Key][]check.Key) ([]check.Key, []dispatchError) {
	in := map[check.Key]bool{}
	for _, k := range selected {
		in[k] = true
	}
	var keep []check.Key
	var held []dispatchError
	for _, k := range selected {
		var early []string
		for _, o := range co[k] {
			if !in[o] {
				early = append(early, o.String())
			}
		}
		if len(early) > 0 {
			held = append(held, dispatchError{k, fmt.Sprintf("not run: running it would also run %s, which this gate does not select", strings.Join(early, ", "))})
			continue
		}
		keep = append(keep, k)
	}
	return keep, held
}

// allowedKeys returns the selected keys and every key that necessarily runs
// with one of them.
func allowedKeys(selected []check.Key, co map[check.Key][]check.Key) map[check.Key]bool {
	allowed := map[check.Key]bool{}
	for _, k := range selected {
		allowed[k] = true
		for _, o := range co[k] {
			allowed[o] = true
		}
	}
	return allowed
}

// validate drops reported outcomes the run cannot have produced: a key
// outside the run, a key reported again, and an unknown outcome. It returns
// the cleaned report, the selected keys whose outcome was unusable with the
// reason, and the problems that fail the gate.
func validate(rep check.Report, name string, src check.Source, s *Survey, keys []check.Key, allowed map[check.Key]bool) (check.Report, map[check.Key]string, []string) {
	var problems []string
	rejected := map[check.Key]string{}
	seen := map[check.Key]bool{}
	accept := func(k check.Key) bool {
		switch {
		case s.Owner[k.Runner] != src || !allowed[k]:
			problems = append(problems, fmt.Sprintf("%s reported %s, which this run did not select", name, k))
		case seen[k]:
			problems = append(problems, fmt.Sprintf("%s reported %s more than once", name, k))
		default:
			seen[k] = true
			return true
		}
		return false
	}
	var results []check.Result
	for _, r := range rep.Results {
		if !accept(r.Key) {
			continue
		}
		switch r.Outcome {
		case check.Pass, check.Fail, check.Blocked:
			results = append(results, r)
		default:
			why := fmt.Sprintf("%s reported the unknown outcome %q", name, r.Outcome)
			if slices.Contains(keys, r.Key) {
				rejected[r.Key] = why // missing reports the key with this reason
			} else {
				problems = append(problems, why+" for "+r.Key.String())
			}
		}
	}
	var skipped []check.Key
	for _, k := range rep.Skipped {
		if accept(k) {
			skipped = append(skipped, k)
		}
	}
	rep.Results, rep.Skipped = results, skipped
	return rep, rejected, problems
}

// missing reports selected keys left without an accepted outcome, with the
// reason: a rejected outcome, or the failed invocation that served the key.
func missing(keys []check.Key, rep check.Report, rejected map[check.Key]string) []dispatchError {
	done := map[check.Key]bool{}
	for _, r := range rep.Results {
		done[r.Key] = true
	}
	for _, k := range rep.Skipped {
		done[k] = true
	}
	var out []dispatchError
	for _, k := range keys {
		if done[k] {
			continue
		}
		detail := noOutcome
		if why, ok := rejected[k]; ok {
			detail = why
		} else {
			for _, inv := range rep.Invocations {
				if inv.Err != nil && slices.Contains(inv.Keys, k) {
					detail = inv.Err.Error()
					break
				}
			}
		}
		out = append(out, dispatchError{k, detail})
	}
	return out
}
