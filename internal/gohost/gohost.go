// Package gohost discovers and runs Go checks: top-level VerifyXxx functions
// in files that build only with the verification tag.
//
// A check has the shape
//
//	func VerifyXxx(ctx context.Context, [files []string], [provided...], w io.Writer) error
//
// and may instead return (T, error) to provide T to checks of the same
// package that take a T parameter. Checks use only standard-library types, so
// a project never depends on this module. Each run compiles a generated
// dispatcher that exists only through a build overlay.
package gohost

import (
	"context"

	"github.com/undervoke/ytif/internal/check"
)

const (
	Runner = "go-verify"
	Tag    = "verification"
)

// Source is the Go host. Discover caches the registry that Run executes.
type Source struct {
	pkgs       []*pkg
	discovered bool
}

func (*Source) Runners() []string { return []string{Runner} }

func (s *Source) Discover(ctx context.Context, repo check.Repo) ([]check.Key, []check.Invocation, error) {
	pkgs, err := discover(ctx, repo)
	s.pkgs, s.discovered = pkgs, true
	var keys []check.Key
	for _, p := range pkgs {
		for _, f := range p.Funcs {
			keys = append(keys, f.Key)
		}
	}
	return keys, nil, err
}

// Run executes keys with their providers. Providers a selection needs run
// even when they are not selected, and their outcomes are reported too.
func (s *Source) Run(ctx context.Context, repo check.Repo, keys []check.Key, in check.Input) (check.Report, error) {
	if !s.discovered {
		if _, _, err := s.Discover(ctx, repo); err != nil && len(s.pkgs) == 0 {
			return check.Report{}, err
		}
	}
	var rep check.Report
	for _, g := range groups(s.pkgs, keys) {
		r := g.run(ctx, repo, in)
		rep.Results = append(rep.Results, r.Results...)
		rep.Invocations = append(rep.Invocations, r.Invocations...)
	}
	return rep, nil
}

// CoRuns maps each check to the providers that run with it.
func (s *Source) CoRuns() map[check.Key][]check.Key {
	out := map[check.Key][]check.Key{}
	for _, p := range s.pkgs {
		for _, f := range p.Funcs {
			seen := map[*fn]bool{}
			var walk func(*fn)
			walk = func(x *fn) {
				for _, d := range x.Deps {
					if !seen[d] {
						seen[d] = true
						out[f.Key] = append(out[f.Key], d.Key)
						walk(d)
					}
				}
			}
			walk(f)
		}
	}
	return out
}
