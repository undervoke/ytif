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
//
// An explicit GIT_WORK_TREE names the work tree, so it keeps authority and
// Root honors the inherited environment first. Otherwise discovery ignores
// GIT_DIR, GIT_WORK_TREE, and GIT_PREFIX so it follows the filesystem from
// dir: a Git hook exports GIT_DIR for its own repository, and once the
// process has moved into a nested directory (for example `go -C
// tools/workspace/verification`), that inherited GIT_DIR makes git treat
// the current directory as the work tree, so --show-toplevel reports the
// nested directory instead of the repository root — or fails when a
// relative GIT_DIR no longer resolves. A bare GIT_DIR cannot locate the
// root after the move, so it never takes precedence. The rest of the
// environment is preserved everywhere, notably GIT_INDEX_FILE, which
// carries the staged view a commit hook checks. When clean discovery finds
// no checkout, Root falls back to the inherited environment.
func Root(dir string) (string, error) {
	if wt, ok := os.LookupEnv("GIT_WORK_TREE"); ok && wt != "" {
		out, err := git(dir, nil, "rev-parse", "--show-toplevel")
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(out)), nil
	}
	out, cleanErr := runGit(dir, nil, discoveryEnv(), "rev-parse", "--show-toplevel")
	if cleanErr == nil {
		return strings.TrimSpace(string(out)), nil
	}
	if out, err := git(dir, nil, "rev-parse", "--show-toplevel"); err == nil {
		return strings.TrimSpace(string(out)), nil
	} else {
		return "", fmt.Errorf("discover repository root for %s: %v (with inherited git environment: %v)", dir, cleanErr, err)
	}
}

// discoveryEnv returns the process environment without the variables that
// override repository discovery. Variables that do not affect discovery —
// the index location, alternate object stores, author identity — pass
// through untouched.
func discoveryEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		if k, _, ok := strings.Cut(kv, "="); ok {
			switch k {
			case "GIT_DIR", "GIT_WORK_TREE", "GIT_PREFIX":
				continue
			}
		}
		env = append(env, kv)
	}
	return env
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
	return runGit(dir, stdin, nil, args...)
}

// runGit runs git -C dir. A nil env inherits the process environment
// unchanged, preserving hook state such as GIT_INDEX_FILE for the commands
// that read it.
func runGit(dir string, stdin io.Reader, env []string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Stdin = stdin
	if env != nil {
		cmd.Env = env
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}
