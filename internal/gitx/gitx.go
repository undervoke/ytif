// Package gitx reads the repository facts that gates bind to.
package gitx

import (
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

func emptyTree(root string) (string, error) {
	out, err := git(root, strings.NewReader(""), "hash-object", "-t", "tree", "--stdin")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
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
// from. Head is the execution HEAD, empty when HEAD is unborn; Branch is the
// checked-out branch, empty when HEAD is detached. Comparison holds the
// resolved commit range, nil when none was resolved; Changed is nil with no
// comparison and empty but non-nil for a valid empty set. CompareHead keeps
// the raw pull-request head apart from Comparison. Target records the
// baseline or target; Fallback names the widening reason, empty when the
// comparison holds or no comparison was needed.
type Snapshot struct {
	Base             string
	Head             string
	Branch           string
	Bases            []string
	Tips             []string
	Changed          []string
	DirtyDigest      string
	Dirty            bool
	RevisionMismatch bool // pushed tips differ from the execution HEAD; a dirty worktree alone sets Dirty instead
	MergeParents     []string
	Comparison       *Comparison   `json:"comparison,omitempty"`
	CompareHead      string        `json:"compare_head,omitempty"`
	Target           TargetContext `json:"target,omitempty"`
	ContextSource    string        `json:"context_source,omitempty"`
	Fallback         string        `json:"fallback,omitempty"`
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
