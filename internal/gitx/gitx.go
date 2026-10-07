// Package gitx reads the repository facts that gates bind to.
package gitx

import (
	"bufio"
	"bytes"
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

// Head returns the current commit hash. It returns "" when the repository
// has no commits yet; provenance is best-effort and records mark it
// unknown.
func Head(root string) string {
	out, err := git(root, nil, "rev-parse", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// CommonDir returns the absolute git common directory, shared by worktrees.
func CommonDir(root string) (string, error) {
	out, err := git(root, nil, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
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
