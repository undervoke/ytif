package check

import "strings"

// TargetScope describes the requested or resolved work, separately from Files.
type TargetScope string

const (
	ScopeFile     TargetScope = "file"
	ScopeAffected TargetScope = "affected"
	ScopeFull     TargetScope = "full"
)

func (s TargetScope) Valid() bool {
	return s == ScopeFile || s == ScopeAffected || s == ScopeFull
}

// Comparison identifies the actual diff; TargetTip is separately projected to Nx.
type Comparison struct {
	Kind string `json:"kind"` // commits, worktree or index
	Base string `json:"base"`
	Head string `json:"head,omitempty"` // commit ranges only
}

type ScopeReport struct {
	Scope   TargetScope `json:"scope"`
	Targets *int        `json:"targets,omitempty"`
}

// Plan keeps policy, offered inputs and verifier-reported execution distinct.
type Plan struct {
	Key       string       `json:"key"`
	Requested TargetScope  `json:"requested"`
	Resolved  TargetScope  `json:"resolved"`
	Origin    string       `json:"origin"`
	Run       bool         `json:"run"`
	Reason    string       `json:"reason,omitempty"`
	Offered   string       `json:"offered"`
	Targets   int          `json:"targets"`
	Expansion string       `json:"expansion,omitempty"`
	Reported  *ScopeReport `json:"reported,omitempty"`
	Changed   []string     `json:"-"`
	Argv      []string     `json:"-"`
}

// RestoreKeys are the ytif-owned variables dispatchers snapshot and restore
// per call. BASE_SHA stays host-owned and is never included.
var RestoreKeys = []string{"YTIF_GATE", "YTIF_PROFILE", "YTIF_COMPARE_KIND", "YTIF_COMPARE_BASE", "YTIF_COMPARE_HEAD", "YTIF_SCOPE", "NX_BASE", "NX_HEAD"}

// HomogeneousScope reports the single resolved scope when every key has a
// plan with the same Resolved value. A missing plan is never homogeneous:
// it runs as full but keeps a distinct process context from actual full.
func (in Input) HomogeneousScope(keys []Key) (TargetScope, bool) {
	var scope TargetScope
	homogeneous := len(keys) > 0
	for _, key := range keys {
		p := in.Plans[key]
		if p == nil || scope != "" && p.Resolved != scope {
			homogeneous = false
			break
		}
		scope = p.Resolved
	}
	return scope, homogeneous
}

// ProcessEnvironment projects only a homogeneous process context. A mixed Go
// batch keeps its host Nx environment; the record carries its per-key plans.
func (in Input) ProcessEnvironment(env []string, keys []Key) []string {
	set := map[string]string{}
	for _, kv := range in.Environment() {
		name, value, _ := strings.Cut(kv, "=")
		set[name] = value
	}
	drop := map[string]bool{"YTIF_SCOPE": true}
	if scope, ok := in.HomogeneousScope(keys); ok {
		set["YTIF_SCOPE"] = string(scope)
		drop["NX_BASE"], drop["NX_HEAD"] = true, true
		if scope != ScopeFull && in.TargetTip != "" && in.Comparison != nil && in.Comparison.Kind == "commits" {
			set["NX_BASE"] = in.TargetTip
		}
	}
	out := make([]string, 0, len(env)+len(set))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if _, replaced := set[name]; !replaced && !drop[name] {
			out = append(out, kv)
		}
	}
	// NX_HEAD never enters set; it is only cleared on homogeneous batches.
	for _, name := range RestoreKeys {
		if value, ok := set[name]; ok {
			out = append(out, name+"="+value)
		}
	}
	return out
}
