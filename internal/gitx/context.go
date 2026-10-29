// Package gitx reads the repository facts that gates bind to.
package gitx

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/undervoke/ytif/internal/proc"
)

// ComparisonOptions selects the comparison GateSnapshot resolves.
// TargetBranch is branch identity: it must match Branch when both are set.
type ComparisonOptions struct {
	Gate           string
	Base           string
	Head           string
	TargetBranch   string
	Remote         string
	Branch         string
	NeedComparison bool
	Stdin          io.Reader
}

// Comparison identifies Changed's baseline and either a commit head, the
// worktree, or the index. Nil means no comparison was resolved.
type Comparison struct {
	Kind string `json:"kind"`
	Base string `json:"base,omitempty"`
	Head string `json:"head,omitempty"`
}

// TargetContext records how a target or baseline was resolved.
type TargetContext struct {
	Remote   string    `json:"remote,omitempty"`
	Branch   string    `json:"branch,omitempty"`
	Given    string    `json:"given,omitempty"`
	Ref      string    `json:"ref,omitempty"`
	SHA      string    `json:"sha,omitempty"`
	Source   string    `json:"source,omitempty"`
	Acquired string    `json:"acquired,omitempty"`
	At       time.Time `json:"at,omitzero"`
}

// GateSnapshot resolves the comparison for gate. Head stays the execution
// HEAD. Explicit refs that do not resolve fail even when NeedComparison is
// false; pushes still disclose tips and mismatches. Nil Changed means no
// comparison; an empty non-nil Changed is a valid empty set.
func GateSnapshot(ctx context.Context, root string, o ComparisonOptions) (Snapshot, error) {
	switch o.Gate {
	case "check", "commit", "push", "ci":
	default:
		return Snapshot{}, fmt.Errorf("unknown gate %q", o.Gate)
	}
	headOut, err := gitCtx(ctx, root, nil, nil, "rev-parse", "--verify", "HEAD")
	unborn := false
	var head string
	if err != nil {
		if _, symErr := gitCtx(ctx, root, nil, nil, "symbolic-ref", "--quiet", "HEAD"); symErr != nil {
			return Snapshot{}, err
		}
		unborn = true
	} else {
		head = strings.TrimSpace(string(headOut))
	}
	if unborn && o.Gate == "push" {
		return Snapshot{}, fmt.Errorf("push gate requires at least one commit: HEAD is unborn")
	}
	var parents []string
	if !unborn {
		parentsOut, err := gitCtx(ctx, root, nil, nil, "log", "--format=%P", "-n", "1", "HEAD")
		if err != nil {
			return Snapshot{}, err
		}
		if f := strings.Fields(strings.TrimSpace(string(parentsOut))); len(f) > 0 {
			parents = f
		}
	}
	digest, dirty, err := dirtyDigest(root, unborn)
	if err != nil {
		return Snapshot{}, err
	}
	branch, err := Branch(root)
	if err != nil {
		return Snapshot{}, err
	}
	snap := Snapshot{Head: head, Branch: branch, DirtyDigest: digest, MergeParents: parents}
	var explicitBase string
	if o.Base != "" {
		b, err := resolveCommit(ctx, root, o.Base)
		if err != nil {
			return Snapshot{}, fmt.Errorf("explicit --base %q: %w", o.Base, err)
		}
		explicitBase = b
	}
	// Declared target identity, validated for shape even on full runs so a
	// full run never conceals a bad target branch.
	var wantBranch, wantRemote, targetRef string
	if o.Gate == "push" || o.Gate == "ci" {
		if o.TargetBranch != "" && o.Branch != "" && o.TargetBranch != o.Branch {
			return Snapshot{}, fmt.Errorf("explicit --target-branch %q does not match declared branch %q", o.TargetBranch, o.Branch)
		}
		wantBranch = firstNonEmpty(o.TargetBranch, o.Branch)
		wantRemote = o.Remote
		if wantRemote == "" && wantBranch != "" {
			wantRemote = "origin"
		}
		if wantBranch != "" {
			if o.Gate == "push" {
				targetRef = "refs/ytif/targets/" + wantRemote + "/" + wantBranch
			} else {
				targetRef = "refs/remotes/" + wantRemote + "/" + wantBranch
			}
			if err := checkRefFormat(ctx, root, targetRef); err != nil {
				return Snapshot{}, fmt.Errorf("target %s/%s: %w", wantRemote, wantBranch, err)
			}
		}
	}
	var explicitHead string
	if o.Gate == "ci" && o.Head != "" {
		h, err := resolveCommit(ctx, root, o.Head)
		if err != nil {
			return Snapshot{}, fmt.Errorf("explicit --head %q: %w", o.Head, err)
		}
		explicitHead = h
	}
	var tips []string
	tipMismatch := false
	if o.Gate == "push" {
		t, err := parsePushTips(ctx, root, o.Stdin, head)
		if err != nil {
			return Snapshot{}, err
		}
		tips = t
		snap.Tips = t
		for _, tip := range t {
			if tip != head {
				tipMismatch = true
				break
			}
		}
		snap.Dirty = dirty
		snap.RevisionMismatch = tipMismatch && len(t) > 0
	}
	// Pull-request identity needs no git objects, so it is established for
	// full runs too. A valid event without a pull_request object follows the
	// non-PR path; malformed JSON stays an error.
	var pr *prEvent
	if o.Gate == "ci" && isPREvent() {
		ev, err := parsePREvent()
		if err != nil {
			return Snapshot{}, err
		}
		if ev != nil {
			if o.Branch != "" && o.Branch != ev.baseRef {
				return Snapshot{}, fmt.Errorf("pull-request event: base ref %q does not match declared branch %q", ev.baseRef, o.Branch)
			}
			if o.TargetBranch != "" && o.TargetBranch != ev.baseRef {
				return Snapshot{}, fmt.Errorf("pull-request event: base ref %q does not match explicit --target-branch %q", ev.baseRef, o.TargetBranch)
			}
			pr = ev
		}
	}
	if !o.NeedComparison {
		// Full runs record observed identity without fetching or diffing.
		now := time.Now().UTC()
		switch {
		case pr != nil:
			snap.Target = TargetContext{Remote: o.Remote, Branch: pr.baseRef, Given: "event pull_request base " + pr.baseRef, SHA: pr.baseSHA, Source: "event", At: now}
			snap.CompareHead = pr.headSHA
			snap.ContextSource = "event"
		case o.Gate == "push" && explicitBase != "":
			snap.Target = TargetContext{Remote: wantRemote, Branch: wantBranch, Given: o.Base, Ref: o.Base, SHA: explicitBase, Source: "explicit", At: now}
			snap.ContextSource = "explicit"
		case o.Gate == "push" && wantBranch != "":
			snap.Target = TargetContext{Remote: wantRemote, Branch: wantBranch, Given: wantRemote + "/" + wantBranch, Ref: targetRef, Source: "profile"}
			snap.ContextSource = "profile"
		case o.Gate == "ci" && explicitBase != "":
			snap.Target = TargetContext{Remote: wantRemote, Branch: wantBranch, Given: o.Base, Ref: o.Base, SHA: explicitBase, Source: "explicit", At: now}
			snap.ContextSource = "explicit"
		case o.Gate == "ci" && wantBranch != "":
			tgt := TargetContext{Remote: wantRemote, Branch: wantBranch, Given: wantRemote + "/" + wantBranch, Ref: targetRef, Source: "local", At: now}
			if sha, err := resolveCommit(ctx, root, targetRef); err == nil {
				tgt.SHA = sha
			} else {
				tgt.SHA, tgt.At = "", time.Time{}
			}
			snap.Target, snap.ContextSource = tgt, "local"
		}
		return snap, nil
	}
	switch o.Gate {
	case "check", "commit":
		var base string
		if explicitBase != "" {
			base = explicitBase
		} else if !unborn {
			base = head
		} else {
			empty, err := emptyTree(root)
			if err != nil {
				return Snapshot{}, err
			}
			base = empty
		}
		var diff []byte
		if o.Gate == "check" {
			diff, err = gitCtx(ctx, root, nil, nil, "diff", "--name-only", "-z", "--no-renames", base)
		} else {
			diff, err = gitCtx(ctx, root, nil, nil, "diff", "--cached", "--name-only", "-z", "--no-renames", base)
		}
		if err != nil {
			return Snapshot{}, err
		}
		changed := unionSorted(splitZ(diff), nil)
		if o.Gate == "check" {
			other, err := gitCtx(ctx, root, nil, nil, "ls-files", "--others", "--exclude-standard", "-z")
			if err != nil {
				return Snapshot{}, err
			}
			changed = unionSorted(changed, splitZ(other))
		}
		if changed == nil {
			changed = []string{}
		}
		now := time.Now().UTC()
		src := "local"
		if explicitBase != "" {
			src = "explicit"
		}
		tgt := TargetContext{SHA: base, Source: src, At: now}
		if explicitBase != "" {
			tgt.Given, tgt.Ref = o.Base, o.Base
		}
		snap.Base = base
		snap.Changed = changed
		kind := "worktree"
		if o.Gate == "commit" {
			kind = "index"
		}
		snap.Comparison = &Comparison{Kind: kind, Base: base}
		snap.Target = tgt
		if o.Gate == "check" {
			snap.ContextSource = "worktree"
		} else {
			snap.ContextSource = "index"
		}
		return snap, nil
	case "push":
		if len(tips) == 0 {
			snap.Changed = []string{}
			snap.ContextSource = "none"
			return snap, nil
		}
		if tipMismatch {
			snap.Fallback = "revision-mismatch"
			return snap, nil
		}
		if dirty {
			snap.Fallback = "dirty-worktree"
			return snap, nil
		}
		now := time.Now().UTC()
		var targetSHA string
		if explicitBase != "" {
			targetSHA = explicitBase
			snap.Target = TargetContext{Remote: wantRemote, Branch: wantBranch, Given: o.Base, Ref: o.Base, SHA: targetSHA, Source: "explicit", At: now}
			snap.ContextSource = "explicit"
		} else {
			if wantBranch == "" {
				snap.Fallback = "no-target"
				return snap, nil
			}
			snap.Target = TargetContext{Remote: wantRemote, Branch: wantBranch, Given: wantRemote + "/" + wantBranch, Ref: targetRef, Source: "fetch"}
			snap.ContextSource = "fetch"
			sha, fetchErr := fetchTarget(ctx, root, wantRemote, wantBranch, targetRef)
			if fetchErr != nil {
				if errors.Is(fetchErr, errCustomSSH) {
					snap.Fallback = "fetch-unavailable: custom ssh preserves transport"
					return snap, nil
				}
				snap.Fallback = "fetch-unavailable: " + shortErr(fetchErr)
				return snap, nil
			}
			targetSHA = sha
			snap.Target.SHA = sha
			snap.Target.Acquired = "fetched"
			snap.Target.At = now
		}
		// Tips are deduplicated; every surviving tip equals HEAD after the
		// mismatch check, so this path has exactly one commit to compare.
		mb, err := mergeBase(ctx, root, targetSHA, tips[0])
		if err != nil {
			snap.Fallback = "merge-base-unavailable: " + shortErr(err)
			return snap, nil
		}
		diff, err := gitCtx(ctx, root, nil, nil, "diff", "--name-only", "-z", "--no-renames", mb, tips[0])
		if err != nil {
			return Snapshot{}, err
		}
		snap.Base, snap.Bases = mb, []string{mb}
		snap.Changed = append([]string{}, unionSorted(splitZ(diff))...)
		snap.Comparison = &Comparison{Kind: "commits", Base: mb, Head: tips[0]}
		return snap, nil
	case "ci":
		if pr != nil {
			// Missing event objects and shallow histories fall back; only a
			// mismatching explicit ref is usage.
			baseCommit, err := resolveCommit(ctx, root, pr.baseSHA)
			if err != nil {
				snap.Target = TargetContext{Remote: o.Remote, Branch: pr.baseRef, Given: "event pull_request base " + pr.baseRef, Source: "event"}
				snap.CompareHead = pr.headSHA
				snap.ContextSource = "event"
				snap.Fallback = "target-unavailable: pull-request event objects not present locally"
				return snap, nil
			}
			headCommit, err := resolveCommit(ctx, root, pr.headSHA)
			if err != nil {
				snap.Target = TargetContext{Remote: o.Remote, Branch: pr.baseRef, Given: "event pull_request base " + pr.baseRef, SHA: baseCommit, Source: "event"}
				snap.CompareHead = pr.headSHA
				snap.ContextSource = "event"
				snap.Fallback = "target-unavailable: pull-request event objects not present locally"
				return snap, nil
			}
			if explicitBase != "" && explicitBase != baseCommit {
				return Snapshot{}, fmt.Errorf("pull-request event: explicit --base %q does not match event base.sha", o.Base)
			}
			if explicitHead != "" && explicitHead != headCommit {
				return Snapshot{}, fmt.Errorf("pull-request event: explicit --head %q does not match event head.sha", o.Head)
			}
			mb, err := mergeBase(ctx, root, baseCommit, headCommit)
			if err != nil {
				snap.Target = TargetContext{Remote: o.Remote, Branch: pr.baseRef, Given: "event pull_request base " + pr.baseRef, SHA: baseCommit, Source: "event"}
				snap.CompareHead = pr.headSHA
				snap.ContextSource = "event"
				snap.Fallback = "merge-base-unavailable: " + shortErr(err)
				return snap, nil
			}
			diff, err := gitCtx(ctx, root, nil, nil, "diff", "--name-only", "-z", "--no-renames", mb, headCommit)
			if err != nil {
				return Snapshot{}, err
			}
			changed := unionSorted(splitZ(diff), nil)
			if changed == nil {
				changed = []string{}
			}
			snap.Base = mb
			snap.Changed = changed
			snap.Comparison = &Comparison{Kind: "commits", Base: mb, Head: headCommit}
			snap.Target = TargetContext{Remote: o.Remote, Branch: pr.baseRef, Given: "event pull_request base " + pr.baseRef, SHA: baseCommit, Source: "event", At: time.Now().UTC()}
			snap.CompareHead = headCommit
			snap.ContextSource = "event"
			if _, err := gitCtx(ctx, root, nil, nil, "merge-base", "--is-ancestor", headCommit, head); err != nil {
				snap.RevisionMismatch = true
				snap.Fallback = "execution-revision-mismatch"
			}
			return snap, nil
		}
		var headOID string
		if explicitHead != "" {
			headOID = explicitHead
		} else {
			if unborn {
				snap.Fallback = "no-head"
				return snap, nil
			}
			headOID = head
		}
		now := time.Now().UTC()
		var targetSHA string
		explicit := explicitBase != "" || explicitHead != "" || o.TargetBranch != ""
		if explicitBase != "" {
			targetSHA = explicitBase
			snap.Target = TargetContext{Remote: wantRemote, Branch: wantBranch, Given: o.Base, Ref: o.Base, SHA: targetSHA, Source: "explicit", At: now}
		} else {
			if wantBranch == "" {
				snap.Fallback = "no-target"
				return snap, nil
			}
			resolved, err := resolveCommit(ctx, root, targetRef)
			if err != nil {
				snap.Target = TargetContext{Remote: wantRemote, Branch: wantBranch, Given: wantRemote + "/" + wantBranch, Ref: targetRef, Source: "local"}
				snap.Fallback = "target-unavailable: " + shortErr(err)
				return snap, nil
			}
			targetSHA = resolved
			snap.Target = TargetContext{Remote: wantRemote, Branch: wantBranch, Given: wantRemote + "/" + wantBranch, Ref: targetRef, SHA: targetSHA, Source: "local", At: now}
		}
		mb, err := mergeBase(ctx, root, targetSHA, headOID)
		if err != nil {
			snap.Fallback = "merge-base-unavailable: " + shortErr(err)
			return snap, nil
		}
		diff, err := gitCtx(ctx, root, nil, nil, "diff", "--name-only", "-z", "--no-renames", mb, headOID)
		if err != nil {
			return Snapshot{}, err
		}
		changed := unionSorted(splitZ(diff), nil)
		if changed == nil {
			changed = []string{}
		}
		snap.Base = mb
		snap.Changed = changed
		snap.Comparison = &Comparison{Kind: "commits", Base: mb, Head: headOID}
		if explicit {
			snap.ContextSource = "explicit"
		} else {
			snap.ContextSource = "local"
		}
		return snap, nil
	}
	return Snapshot{}, fmt.Errorf("unknown gate %q", o.Gate)
}

// isPREvent reports a pull-request CI event.
func isPREvent() bool {
	switch strings.TrimSpace(os.Getenv("GITHUB_EVENT_NAME")) {
	case "pull_request", "pull_request_target":
		return true
	}
	return false
}

// prEvent carries the observed pull-request identity: raw SHAs as the event
// claims them, resolved only where a diff needs them.
type prEvent struct {
	baseRef string
	baseSHA string
	headSHA string
}

// parsePREvent reads the event file. Malformed JSON is an error; a valid
// event without a pull_request object is not a PR run.
func parsePREvent() (*prEvent, error) {
	path := strings.TrimSpace(os.Getenv("GITHUB_EVENT_PATH"))
	if path == "" {
		return nil, fmt.Errorf("pull-request event: GITHUB_EVENT_PATH is not set")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("pull-request event: %w", err)
	}
	var event struct {
		PullRequest *struct {
			Base *struct {
				Ref string `json:"ref"`
				SHA string `json:"sha"`
			} `json:"base"`
			Head *struct {
				SHA string `json:"sha"`
			} `json:"head"`
		} `json:"pull_request"`
	}
	if err := json.Unmarshal(data, &event); err != nil {
		return nil, fmt.Errorf("pull-request event: %w", err)
	}
	if event.PullRequest == nil {
		return nil, nil
	}
	if event.PullRequest.Base == nil || event.PullRequest.Head == nil ||
		strings.TrimSpace(event.PullRequest.Base.Ref) == "" ||
		strings.TrimSpace(event.PullRequest.Base.SHA) == "" ||
		strings.TrimSpace(event.PullRequest.Head.SHA) == "" {
		return nil, fmt.Errorf("pull-request event: missing pull_request.base.ref, pull_request.base.sha, or pull_request.head.sha")
	}
	return &prEvent{
		baseRef: strings.TrimSpace(event.PullRequest.Base.Ref),
		baseSHA: strings.TrimSpace(event.PullRequest.Base.SHA),
		headSHA: strings.TrimSpace(event.PullRequest.Head.SHA),
	}, nil
}

// parsePushTips lists non-deleted pushed tips without duplicates, peeling
// tags. With no ref lines it falls back to head.
func parsePushTips(ctx context.Context, root string, pushRefs io.Reader, head string) ([]string, error) {
	sawLine := false
	seen := map[string]bool{}
	var tips []string
	if pushRefs != nil {
		sc := bufio.NewScanner(pushRefs)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			f := strings.Fields(sc.Text())
			if len(f) != 4 {
				continue
			}
			sawLine = true
			if zeroOID(f[1]) {
				continue
			}
			peeled, err := resolveCommit(ctx, root, f[1])
			if err != nil {
				return nil, fmt.Errorf("push tip %q: %w", f[1], err)
			}
			if !seen[peeled] {
				seen[peeled] = true
				tips = append(tips, peeled)
			}
		}
		if err := sc.Err(); err != nil {
			return nil, fmt.Errorf("read push refs: %w", err)
		}
	}
	if !sawLine {
		return []string{head}, nil
	}
	if tips == nil {
		tips = []string{}
	}
	return tips, nil
}

// errCustomSSH reports a fetch skipped to preserve a custom SSH transport.
var errCustomSSH = errors.New("custom ssh preserves transport")

// fetchTarget fetches branch into the ytif-owned private ref. The forced
// refspec follows rewound targets; user refs and FETCH_HEAD are untouched,
// the remote is addressed by name, and history is never deepened.
func fetchTarget(ctx context.Context, root, remote, branch, privateRef string) (string, error) {
	if sshRemote(RemoteURL(root, remote)) && hasCustomSSH(ctx, root) {
		return "", errCustomSSH
	}
	fetchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	env := []string{"GIT_TERMINAL_PROMPT=0", "GIT_SSH_COMMAND=ssh -o BatchMode=yes"}
	if _, err := gitCtx(fetchCtx, root, nil, env, "-c", "gc.auto=0", "-c", "maintenance.auto=false", "fetch", "--no-tags", "--no-recurse-submodules", "--no-write-fetch-head", remote, "+"+"refs/heads/"+branch+":"+privateRef); err != nil {
		return "", err
	}
	out, err := gitCtx(ctx, root, nil, nil, "rev-parse", "--verify", privateRef+"^{commit}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// sshRemote reports an SSH-transport remote URL: ssh://... or scp-like
// [user@]host:path. Local paths and other schemes are not SSH.
func sshRemote(url string) bool {
	url = strings.TrimSpace(url)
	if url == "" {
		return false
	}
	if strings.Contains(url, "://") {
		scheme, _, _ := strings.Cut(url, "://")
		return scheme == "ssh" || strings.HasSuffix(scheme, "+ssh")
	}
	if strings.HasPrefix(url, "/") || strings.HasPrefix(url, ".") {
		return false
	}
	if len(url) > 2 && url[1] == ':' {
		return false
	}
	host, rest, ok := strings.Cut(url, ":")
	if !ok || host == "" || strings.Contains(host, "/") || rest == "" {
		return false
	}
	return true
}

// hasCustomSSH reports configured custom SSH that a fetch must not replace.
func hasCustomSSH(ctx context.Context, root string) bool {
	if strings.TrimSpace(os.Getenv("GIT_SSH_COMMAND")) != "" || strings.TrimSpace(os.Getenv("GIT_SSH")) != "" {
		return true
	}
	if out, err := gitCtx(ctx, root, nil, nil, "config", "--get", "core.sshCommand"); err == nil && strings.TrimSpace(string(out)) != "" {
		return true
	}
	return false
}

func resolveCommit(ctx context.Context, root, rev string) (string, error) {
	out, err := gitCtx(ctx, root, nil, nil, "rev-parse", "--verify", rev+"^{commit}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func mergeBase(ctx context.Context, root, a, b string) (string, error) {
	out, err := gitCtx(ctx, root, nil, nil, "merge-base", a, b)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func checkRefFormat(ctx context.Context, root, ref string) error {
	_, err := gitCtx(ctx, root, nil, nil, "check-ref-format", ref)
	return err
}

// gitCtx keeps the hook's Git context while cancellation stops its process group.
func gitCtx(ctx context.Context, dir string, stdin io.Reader, env []string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Stdin = stdin
	if env != nil {
		cmd.Env = append(os.Environ(), env...)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if _, err := proc.RunGit(ctx, cmd); err != nil {
		return nil, fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func shortErr(err error) string {
	if err == nil {
		return ""
	}
	s := strings.TrimSpace(err.Error())
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	return s
}
