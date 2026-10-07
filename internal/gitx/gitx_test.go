package gitx_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// TestRootFromRealHook installs a real pre-commit hook in a linked-worktree
// fixture shaped like the wide-projects checkout — nested tool module,
// absolute GIT_DIR at the worktree admin dir — and fires it with a commit,
// so the probe starts from the nested directory with the hook environment
// exactly as git sets it. The probe reports the resolved root, the staged
// files visible from it, and the inherited location variables, which also
// proves the run exercises the genuine hook environment rather than a
// hand-built one.
func TestRootFromRealHook(t *testing.T) {
	probe := buildProbe(t)
	origin := newRepo(t)
	linked := filepath.Join(t.TempDir(), "linked")
	runGit(t, origin, "worktree", "add", linked)
	t.Cleanup(func() { runGit(t, origin, "worktree", "remove", "--force", linked) })
	nested := filepath.Join(linked, "tools", "workspace", "verification")
	admin := gitDir(t, linked)
	want := physical(t, linked)

	installHook(t, linked)
	commit := func(phase string, extra []string, allowEmpty bool) probeReport {
		t.Helper()
		out := filepath.Join(t.TempDir(), "probe.out")
		env := append(os.Environ(),
			"YTIF_HOOK_NESTED="+nested,
			"YTIF_HOOK_PROBE="+probe,
			"YTIF_HOOK_OUT="+out,
			"YTIF_HOOK_PHASE="+phase,
		)
		env = append(env, extra...)
		args := []string{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-m", phase}
		if allowEmpty {
			args = append(args, "--allow-empty")
		}
		cmd := exec.Command("git", args...)
		cmd.Dir = linked
		cmd.Env = env
		if bout, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("phase %s: %v\n%s", phase, err, bout)
		}
		raw, err := os.ReadFile(out)
		if err != nil {
			t.Fatalf("phase %s: read probe output: %v", phase, err)
		}
		return parseProbe(t, string(raw))
	}

	t.Run("resolves worktree root with genuine hook environment", func(t *testing.T) {
		writeFile(t, filepath.Join(linked, "staged.txt"), "v2\n", 0o644)
		runGit(t, linked, "add", "staged.txt")
		rep := commit("resolves", nil, false)
		if rep.env["GIT_DIR"] != admin {
			t.Fatalf("hook GIT_DIR = %q, want admin dir %q", rep.env["GIT_DIR"], admin)
		}
		if rep.env["GIT_PREFIX"] != "" {
			t.Fatalf("hook GIT_PREFIX = %q, want empty", rep.env["GIT_PREFIX"])
		}
		if idx := rep.env["GIT_INDEX_FILE"]; idx != "" && idx != filepath.Join(admin, "index") {
			t.Fatalf("hook GIT_INDEX_FILE = %q, want the worktree index", idx)
		}
		if physical(t, rep.root) != want {
			t.Fatalf("ROOT = %q, want worktree root %q", rep.root, want)
		}
		if !slices.Equal(rep.staged, []string{"staged.txt"}) {
			t.Fatalf("STAGED = %q, want [staged.txt]", rep.staged)
		}
	})

	t.Run("resolves with relative git dir", func(t *testing.T) {
		rep := commit("relative", nil, true)
		if physical(t, rep.root) != want {
			t.Fatalf("ROOT = %q, want worktree root %q", rep.root, want)
		}
	})

	t.Run("explicit work tree keeps authority", func(t *testing.T) {
		other := t.TempDir()
		rep := commit("worktree", []string{"YTIF_HOOK_WORKTREE=" + other}, true)
		if physical(t, rep.root) != physical(t, other) {
			t.Fatalf("ROOT = %q, want configured work tree %q", rep.root, other)
		}
	})

	t.Run("reads an alternate index", func(t *testing.T) {
		writeFile(t, filepath.Join(linked, "custom-src.txt"), "custom\n", 0o644)
		blob := strings.TrimSpace(gitOut(t, linked, "hash-object", "-w", "custom-src.txt"))
		idx := filepath.Join(t.TempDir(), "index")
		alt := []string{"GIT_DIR=" + admin, "GIT_INDEX_FILE=" + idx}
		runGitEnv(t, linked, alt, "read-tree", "--empty")
		runGitEnv(t, linked, alt, "update-index", "--add", "--cacheinfo", "100644,"+blob+",custom.txt")
		cmd := exec.Command(probe)
		cmd.Dir = linked
		cmd.Env = append(os.Environ(), alt...)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("probe with alternate index: %v", err)
		}
		rep := parseProbe(t, string(out))
		if !slices.Equal(rep.staged, []string{"custom.txt"}) {
			t.Fatalf("STAGED = %q, want [custom.txt] from the alternate index", rep.staged)
		}
	})
}

// probeReport is one parsed hookprobe run.
type probeReport struct {
	root   string
	staged []string
	env    map[string]string
}

func parseProbe(t *testing.T, raw string) probeReport {
	t.Helper()
	rep := probeReport{env: map[string]string{}}
	for line := range strings.Lines(strings.TrimSpace(raw)) {
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		switch {
		case strings.HasPrefix(line, "ROOT="):
			rep.root = strings.TrimPrefix(line, "ROOT=")
		case strings.HasPrefix(line, "STAGED="):
			rep.staged = append(rep.staged, strings.TrimPrefix(line, "STAGED="))
		case strings.HasPrefix(line, "ENV "):
			k, v, _ := strings.Cut(strings.TrimPrefix(line, "ENV "), "=")
			rep.env[k] = v
		case strings.HasPrefix(line, "ROOT-ERR:"), strings.HasPrefix(line, "STAGED-ERR:"):
			t.Fatalf("probe failed: %s", line)
		default:
			t.Fatalf("unparsed probe line: %q", line)
		}
	}
	if rep.root == "" {
		t.Fatalf("probe reported no root:\n%s", raw)
	}
	return rep
}

// installHook writes a pre-commit hook for the worktree. Linked worktrees
// share hooks through the common directory, so the path comes from
// --git-path. The hook moves into the nested module directory and execs
// the probe, keeping the hook-inherited environment; phases rewrite one
// variable first.
func installHook(t *testing.T, linked string) {
	t.Helper()
	script := "#!/bin/sh\n" +
		"case \"$YTIF_HOOK_PHASE\" in\n" +
		"  relative) export GIT_DIR=.git;;\n" +
		"  worktree) export GIT_WORK_TREE=\"$YTIF_HOOK_WORKTREE\";;\n" +
		"esac\n" +
		"cd \"$YTIF_HOOK_NESTED\" || exit 99\n" +
		"exec \"$YTIF_HOOK_PROBE\" >\"$YTIF_HOOK_OUT\"\n"
	dir := strings.TrimSpace(gitOut(t, linked, "rev-parse", "--git-path", "hooks"))
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(linked, dir)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "pre-commit"), script, 0o755)
}

// newRepo builds a repository with a nested tool module directory.
func newRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	runGit(t, root, "-c", "user.email=t@t", "-c", "user.name=t", "-c", "init.defaultBranch=main", "init")
	nested := filepath.Join(root, "tools", "workspace", "verification")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(nested, "go.mod"), "module example.com/tool\n", 0o644)
	writeFile(t, filepath.Join(root, "README.md"), "hi\n", 0o644)
	runGit(t, root, "add", ".")
	runGit(t, root, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-m", "init")
	return root
}

// buildProbe compiles the hookprobe helper inside the ytif module.
func buildProbe(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("find test source")
	}
	root := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(root)
		if parent == root {
			t.Fatal("find module root")
		}
		root = parent
	}
	bin := filepath.Join(t.TempDir(), "hookprobe")
	cmd := exec.Command("go", "build", "-o", bin, "./internal/hookprobe")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build hookprobe: %v\n%s", err, out)
	}
	return bin
}

func gitDir(t *testing.T, dir string) string {
	t.Helper()
	return strings.TrimSpace(gitOut(t, dir, "rev-parse", "--absolute-git-dir"))
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(out)
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	runGitEnv(t, dir, nil, args...)
}

func runGitEnv(t *testing.T, dir string, extra []string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if extra != nil {
		cmd.Env = append(os.Environ(), extra...)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func writeFile(t *testing.T, path, body string, perm os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), perm); err != nil {
		t.Fatal(err)
	}
}

func physical(t *testing.T, path string) string {
	t.Helper()
	p, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
