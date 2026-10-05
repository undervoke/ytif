package gate

import (
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/undervoke/ytif/internal/check"
	"github.com/undervoke/ytif/internal/reconcile"
)

// findingLine matches path:line[:col]: message, the format checks use for
// findings.
var findingLine = regexp.MustCompile(`^([^\s:][^:]*):(\d+):(?:(\d+):)?\s*(.*)$`)

// present prints outcomes, dispatch errors, and findings, emits GitHub
// annotations when requested, and returns the exit code.
func present(o Options, run outcome, findings []reconcile.Finding) int {
	w := o.Stdout
	reports, dispatch := run.reports, run.dispatch
	github := o.Report == "github"
	var passed, failed, blocked, skipped int
	for _, rep := range reports {
		for _, r := range rep.Results {
			switch r.Outcome {
			case check.Pass:
				passed++
			case check.Fail:
				failed++
			case check.Blocked:
				blocked++
			}
			fmt.Fprintf(w, "%-8s %s%s\n", strings.ToUpper(string(r.Outcome)), r.Key, elapsed(r))
			if r.Outcome == check.Fail {
				printOutput(w, r.Output)
				if github {
					annotate(w, r.Key.String(), r.Output)
				}
			}
		}
		for _, k := range rep.Skipped {
			skipped++
			fmt.Fprintf(w, "%-8s %s\n", "SKIP", k)
		}
	}
	// One failed build can leave many checks without an outcome; print each
	// distinct cause once, after the checks it affected.
	sorted := slices.Clone(dispatch)
	slices.SortStableFunc(sorted, func(a, b dispatchError) int { return strings.Compare(a.detail, b.detail) })
	var affected []string
	for i, d := range sorted {
		fmt.Fprintf(w, "%-8s %s\n", "ERROR", d.key)
		affected = append(affected, d.key.String())
		if i+1 < len(sorted) && sorted[i+1].detail == d.detail {
			continue
		}
		printOutput(w, d.detail)
		if github {
			title := escapeProp(fmt.Sprintf("ytif: %d checks without outcome", len(affected)))
			fmt.Fprintf(w, "::warning title=%s::%s\n", title, escapeData(strings.Join(affected, ", ")+"\n"+d.detail))
		}
		affected = nil
	}
	// Invocation failures that explain no missing outcome still fail the
	// gate: a package that fails outside its tests, or a crash after the
	// last result.
	explained := map[string]bool{}
	for _, d := range dispatch {
		explained[d.detail] = true
	}
	invErrors := 0
	for _, rep := range reports {
		for _, inv := range rep.Invocations {
			if inv.Err == nil || explained[inv.Err.Error()] {
				continue
			}
			invErrors++
			fmt.Fprintf(w, "%-8s %s\n", "ERROR", strings.TrimSpace(inv.Runner+" "+inv.What+" "+inv.Unit))
			printOutput(w, inv.Err.Error())
			if github {
				title := escapeProp(fmt.Sprintf("ytif: %s %s failed", inv.Runner, inv.What))
				fmt.Fprintf(w, "::warning title=%s::%s\n", title, escapeData(inv.Unit+"\n"+inv.Err.Error()))
			}
		}
	}
	for _, p := range run.problems {
		fmt.Fprintf(w, "%-8s %s\n", "ERROR", p)
		if github {
			fmt.Fprintf(w, "::warning title=ytif::%s\n", escapeData(p))
		}
	}
	if len(findings) > 0 {
		fmt.Fprintf(w, "reconciliation:\n")
		for _, f := range findings {
			fmt.Fprintf(w, "  %s\n", f)
			if github {
				fmt.Fprintf(w, "::warning title=ytif reconciliation::%s\n", escapeData(f.String()))
			}
		}
	}

	failing := failed > 0 || len(dispatch) > 0 || invErrors > 0 || len(run.problems) > 0
	if len(findings) > 0 {
		failing = true
	}
	parts := []string{fmt.Sprintf("%d passed", passed)}
	for _, p := range []struct {
		n    int
		what string
	}{{failed, "failed"}, {blocked, "blocked"}, {skipped, "skipped"}, {len(dispatch), "without outcome"}, {invErrors + len(run.problems), "errors"}, {len(findings), "findings"}} {
		if p.n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", p.n, p.what))
		}
	}
	verdict := "pass"
	if failing {
		verdict = "FAIL"
	}
	fmt.Fprintf(w, "ytif %s: %s (%s)\n", o.Gate, verdict, strings.Join(parts, ", "))
	if failing {
		return ExitFail
	}
	return ExitPass
}

func elapsed(r check.Result) string {
	switch {
	case !r.Timed:
		return ""
	case r.Elapsed < time.Millisecond:
		return "  <1ms"
	}
	return "  " + r.Elapsed.Round(time.Millisecond).String()
}

func printOutput(w io.Writer, out string) {
	out = strings.TrimRight(out, "\n")
	if out == "" {
		return
	}
	for _, line := range strings.Split(out, "\n") {
		fmt.Fprintf(w, "         %s\n", line)
	}
}

// annotate turns path:line findings into GitHub warnings, and always adds one
// summary warning so output without locations still surfaces.
func annotate(w io.Writer, key, out string) {
	title := escapeProp("ytif " + key)
	for _, line := range strings.Split(out, "\n") {
		m := findingLine.FindStringSubmatch(strings.TrimRight(line, "\r"))
		if m == nil {
			continue
		}
		props := "file=" + escapeProp(m[1]) + ",line=" + m[2]
		if m[3] != "" {
			props += ",col=" + m[3]
		}
		fmt.Fprintf(w, "::warning %s,title=%s::%s\n", props, title, escapeData(m[4]))
	}
	fmt.Fprintf(w, "::warning title=%s::check failed\n", title)
}

var (
	dataEscaper = strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A")
	propEscaper = strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A", ":", "%3A", ",", "%2C")
)

func escapeData(s string) string { return dataEscaper.Replace(s) }
func escapeProp(s string) string { return propEscaper.Replace(s) }
