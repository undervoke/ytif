// Package gitx reads the repository facts that gates bind to.
package gitx

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Root returns the top-level directory of the repository containing dir.
func Root(dir string) (string, error) {
	out, err := git(dir, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// CommonDir returns the absolute git common directory, shared by worktrees.
func CommonDir(root string) (string, error) {
	out, err := git(root, nil, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// Branch returns the checked-out branch name. A detached HEAD returns an
// empty name, which is how CI checkouts usually arrive.
func Branch(root string) (string, error) {
	// symbolic-ref exits 1, printing nothing, on a detached HEAD, so the exit
	// code has to survive to here.
	cmd := exec.Command("git", "-C", root, "symbolic-ref", "--quiet", "--short", "HEAD")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 && stderr.Len() == 0 {
			return "", nil
		}
		return "", fmt.Errorf("git symbolic-ref HEAD: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

// RemoteURL returns the URL of a named remote. A remote that is not
// configured reads as empty, so a repository without one is not an error.
func RemoteURL(root, name string) string {
	out, err := git(root, nil, "remote", "get-url", name)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// Tracked lists tracked files that exist on disk as files.
func Tracked(root string) ([]string, error) {
	out, err := git(root, nil, "ls-files", "-z", "--cached")
	if err != nil {
		return nil, err
	}
	var files []string
	seen := map[string]bool{}
	for _, p := range splitZ(out) {
		if seen[p] {
			continue // unmerged paths repeat once per stage
		}
		seen[p] = true
		info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(p)))
		if err != nil || info.IsDir() {
			continue // deleted in the worktree, or a submodule
		}
		files = append(files, p)
	}
	return files, nil
}

// Staged lists staged additions, copies, modifications, and renames.
func Staged(root string) ([]string, error) {
	out, err := git(root, nil, "diff", "--cached", "--name-only", "-z", "--diff-filter=ACMR")
	if err != nil {
		return nil, err
	}
	return splitZ(out), nil
}

// Pushed lists files changed by a push. refs carries pre-push hook lines
// "<local ref> <local oid> <remote ref> <remote oid>". Without any line, the
// push target is @{push}, else origin/HEAD, compared with HEAD. Ref
// deletions contribute no files, and new remote refs compare with the empty
// tree.
func Pushed(root string, refs io.Reader) ([]string, error) {
	empty, err := emptyTree(root)
	if err != nil {
		return nil, err
	}
	type pair struct{ base, tip string }
	var pairs []pair
	sawLine := false
	if refs != nil {
		sc := bufio.NewScanner(refs)
		for sc.Scan() {
			f := strings.Fields(sc.Text())
			if len(f) != 4 {
				continue
			}
			sawLine = true
			local, remote := f[1], f[3]
			if zeroOID(local) {
				continue
			}
			base := remote
			if zeroOID(remote) || !hasCommit(root, remote) {
				base = empty
			}
			pairs = append(pairs, pair{base, local})
		}
		if err := sc.Err(); err != nil {
			return nil, fmt.Errorf("read push refs: %w", err)
		}
	}
	if !sawLine {
		base := empty
		for _, ref := range []string{"@{push}", "origin/HEAD"} {
			if out, err := git(root, nil, "rev-parse", "--verify", "--quiet", ref); err == nil {
				base = strings.TrimSpace(string(out))
				break
			}
		}
		pairs = append(pairs, pair{base, "HEAD"})
	}
	seen := map[string]bool{}
	var files []string
	for _, p := range pairs {
		out, err := git(root, nil, "diff", "--name-only", "-z", "--diff-filter=ACMR", p.base, p.tip)
		if err != nil {
			return nil, err
		}
		for _, f := range splitZ(out) {
			if !seen[f] {
				seen[f] = true
				files = append(files, f)
			}
		}
	}
	sort.Strings(files)
	return files, nil
}

func emptyTree(root string) (string, error) {
	out, err := git(root, strings.NewReader(""), "hash-object", "-t", "tree", "--stdin")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func hasCommit(root, oid string) bool {
	_, err := git(root, nil, "cat-file", "-e", oid+"^{commit}")
	return err == nil
}

func zeroOID(oid string) bool {
	return oid != "" && strings.Trim(oid, "0") == ""
}

func splitZ(out []byte) []string {
	var parts []string
	for _, p := range bytes.Split(out, []byte{0}) {
		if len(p) > 0 {
			parts = append(parts, string(p))
		}
	}
	return parts
}

func git(dir string, stdin io.Reader, args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Stdin = stdin
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// MergeBase returns the best common ancestor of ref and HEAD.
func MergeBase(root, ref string) (string, error) {
	out, err := git(root, nil, "merge-base", ref, "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// Snapshot describes the revision and change context a gate run selects
// from. Base is the merge-base of baseRef with HEAD, empty when no base was
// requested. Head is the actual HEAD, empty when HEAD is unborn; Branch is
// the checked-out branch, empty when HEAD is detached. Full
// check/commit/ci runs work before the first commit and snapshot the dirty
// state against the empty tree instead. Bases holds one merge-base per
// pushed tip for the push gate. Tips holds the non-deleted pushed tips, or
// HEAD when push supplied no refs. Changed is the NUL-safe sorted unique
// change set for affected selection, nil when no base was requested.
// DirtyDigest hashes the actual local changed content. RevisionMismatch
// reports a push run whose execution revision differs from a pushed tip or
// whose tracked worktree differs from HEAD. MergeParents records the parents
// of HEAD.
type Snapshot struct {
	Base             string
	Head             string
	Branch           string
	Bases            []string
	Tips             []string
	Changed          []string
	DirtyDigest      string
	RevisionMismatch bool
	MergeParents     []string
}

// SelectionSnapshot resolves the changed-file context for gate with an
// optional --base ref. An empty baseRef requests a full run: it still
// snapshots HEAD, parents, tips, and the dirty digest but resolves no
// comparison. A non-empty baseRef must resolve to a commit even when push
// supplies zero tips. It does not infer a PR target for push.
func SelectionSnapshot(root, gate, baseRef string, pushRefs io.Reader) (Snapshot, error) {
	switch gate {
	case "check", "commit", "push", "ci":
	default:
		return Snapshot{}, fmt.Errorf("unknown gate %q", gate)
	}
	headOut, err := git(root, nil, "rev-parse", "--verify", "HEAD")
	unborn := false
	var head string
	if err != nil {
		// A fresh repository has a symbolic HEAD but no commit to
		// resolve. Treat that as an unborn HEAD; anything else keeps
		// the original resolution error.
		if _, symErr := git(root, nil, "symbolic-ref", "--quiet", "HEAD"); symErr != nil {
			return Snapshot{}, err
		}
		unborn = true
	} else {
		head = strings.TrimSpace(string(headOut))
	}
	if unborn && gate == "push" {
		return Snapshot{}, fmt.Errorf("push gate requires at least one commit: HEAD is unborn")
	}
	var parents []string
	if !unborn {
		parentsOut, err := git(root, nil, "log", "--format=%P", "-n", "1", "HEAD")
		if err != nil {
			return Snapshot{}, err
		}
		if f := strings.Fields(strings.TrimSpace(string(parentsOut))); len(f) > 0 {
			parents = f
		}
	}
	digest, worktreeDirty, err := dirtyDigest(root, unborn)
	if err != nil {
		return Snapshot{}, err
	}
	branch, err := Branch(root)
	if err != nil {
		return Snapshot{}, err
	}
	snap := Snapshot{Head: head, Branch: branch, DirtyDigest: digest, MergeParents: parents}
	if baseRef == "" {
		if gate == "push" {
			tips, err := pushTips(root, pushRefs, head)
			if err != nil {
				return Snapshot{}, err
			}
			snap.Tips = tips
			snap.RevisionMismatch = pushMismatch(tips, head, worktreeDirty)
		}
		return snap, nil
	}
	if _, err := git(root, nil, "rev-parse", "--verify", baseRef+"^{commit}"); err != nil {
		return Snapshot{}, err
	}
	switch gate {
	case "check", "commit":
		base, err := MergeBase(root, baseRef)
		if err != nil {
			return Snapshot{}, err
		}
		snap.Base = base
		work, err := git(root, nil, "diff", "--name-only", "-z", "--no-renames", base)
		if err != nil {
			return Snapshot{}, err
		}
		other, err := git(root, nil, "ls-files", "--others", "--exclude-standard", "-z")
		if err != nil {
			return Snapshot{}, err
		}
		snap.Changed = unionSorted(splitZ(work), splitZ(other))
	case "push":
		tips, err := pushTips(root, pushRefs, head)
		if err != nil {
			return Snapshot{}, err
		}
		snap.Tips = tips
		snap.RevisionMismatch = pushMismatch(tips, head, worktreeDirty)
		base, err := MergeBase(root, baseRef)
		if err != nil {
			return Snapshot{}, err
		}
		snap.Base = base
		if len(tips) == 0 {
			snap.Changed = []string{}
			return snap, nil
		}
		bases := make([]string, 0, len(tips))
		var changed []string
		seen := map[string]bool{}
		for _, tip := range tips {
			mbOut, err := git(root, nil, "merge-base", baseRef, tip)
			if err != nil {
				return Snapshot{}, err
			}
			mb := strings.TrimSpace(string(mbOut))
			bases = append(bases, mb)
			diff, err := git(root, nil, "diff", "--name-only", "-z", "--no-renames", mb, tip)
			if err != nil {
				return Snapshot{}, err
			}
			for _, f := range splitZ(diff) {
				if !seen[f] {
					seen[f] = true
					changed = append(changed, f)
				}
			}
		}
		snap.Bases = bases
		sort.Strings(changed)
		snap.Changed = changed
	case "ci":
		base, err := MergeBase(root, baseRef)
		if err != nil {
			return Snapshot{}, err
		}
		snap.Base = base
		diff, err := git(root, nil, "diff", "--name-only", "-z", "--no-renames", base, "HEAD")
		if err != nil {
			return Snapshot{}, err
		}
		snap.Changed = unionSorted(splitZ(diff), nil)
	}
	return snap, nil
}

// pushTips lists non-deleted push tips in ref order without duplicates. With
// no ref lines it falls back to head.
func pushTips(root string, pushRefs io.Reader, head string) ([]string, error) {
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
			local := f[1]
			if zeroOID(local) {
				continue
			}
			if !seen[local] {
				seen[local] = true
				tips = append(tips, local)
			}
		}
		if err := sc.Err(); err != nil {
			return nil, fmt.Errorf("read push refs: %w", err)
		}
	}
	if !sawLine {
		return []string{head}, nil
	}
	return tips, nil
}

// pushMismatch reports a push run that would not execute a pushed tip as-is:
// a tip differs from HEAD, tracked content differs, or nonignored untracked
// files are present. Directory-scanning verifiers can read those files too.
func pushMismatch(tips []string, head string, worktreeDirty bool) bool {
	if len(tips) == 0 {
		return false
	}
	for _, tip := range tips {
		if tip != head {
			return true
		}
	}
	return worktreeDirty
}

// unionSorted merges NUL-safe path lists into a sorted unique list.
func unionSorted(lists ...[]string) []string {
	seen := map[string]bool{}
	var out []string
	for _, list := range lists {
		for _, f := range list {
			if f == "" || seen[f] {
				continue
			}
			seen[f] = true
			out = append(out, f)
		}
	}
	sort.Strings(out)
	return out
}

// dirtyDigest hashes the actual local changed content deterministically:
// the full tracked patch against HEAD (or the empty tree when HEAD is
// unborn) plus every untracked file's name and complete contents. It streams
// whole files with no read limit so two different worktrees cannot share a
// digest through truncation. With an unborn HEAD the index patch is hashed
// too, so a staged file deleted from the worktree still moves the digest.
func dirtyDigest(root string, unborn bool) (string, bool, error) {
	against := "HEAD"
	var staged []byte
	if unborn {
		empty, err := emptyTree(root)
		if err != nil {
			return "", false, err
		}
		against = empty
		staged, err = git(root, nil, "diff", "--cached", against, "--no-color", "--no-ext-diff", "--no-renames", "--binary", "--full-index")
		if err != nil {
			return "", false, err
		}
	}
	patch, err := git(root, nil, "diff", against, "--no-color", "--no-ext-diff", "--no-renames", "--binary", "--full-index")
	if err != nil {
		return "", false, err
	}
	out, err := git(root, nil, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return "", false, err
	}
	untracked := splitZ(out)
	sort.Strings(untracked)
	h := sha256.New()
	h.Write(patch)
	h.Write([]byte{0})
	h.Write(staged)
	h.Write([]byte{0})
	for _, f := range untracked {
		h.Write([]byte(f))
		h.Write([]byte{0})
		p := filepath.Join(root, filepath.FromSlash(f))
		fi, err := os.Lstat(p)
		if err != nil {
			continue // vanished between listing and hashing; the name is recorded
		}
		if fi.IsDir() {
			continue
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(p)
			if err != nil {
				continue
			}
			h.Write([]byte(target))
			h.Write([]byte{0})
			continue
		}
		fh, err := os.Open(p)
		if err != nil {
			continue
		}
		_, copyErr := io.Copy(h, fh)
		fh.Close()
		if copyErr != nil {
			return "", false, copyErr
		}
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), len(patch) > 0 || len(staged) > 0 || len(untracked) > 0, nil
}
