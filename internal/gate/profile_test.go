package gate_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/undervoke/ytif/internal/adapter"
	"github.com/undervoke/ytif/internal/check"
	"github.com/undervoke/ytif/internal/gate"
	"github.com/undervoke/ytif/internal/gateconf"
	"github.com/undervoke/ytif/internal/gohost"
	"github.com/undervoke/ytif/internal/runner/dotnet"
	"github.com/undervoke/ytif/internal/runner/gotest"
	"github.com/undervoke/ytif/internal/runner/jstest"
	"github.com/undervoke/ytif/internal/testhelp"
)

// fixture is an isolated git repository with an inventory, an execution
// list, and a fake adapter subprocess answering from canned replies.
type fixture struct {
	root string
	bin  string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	runGit(t, root, "-c", "user.email=t@t", "-c", "user.name=t", "-c", "init.defaultBranch=main", "init")
	write(t, root, "app.txt", "v1\n")
	// A Go test outside any module: native go-test discovery always
	// reports it, so the full-gate control genuinely needs the toolchain.
	write(t, root, "ok_test.go", "package x\n\nimport \"testing\"\n\nfunc TestOk(t *testing.T) {}\n")
	runGit(t, root, "add", "app.txt", "ok_test.go")
	runGit(t, root, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-m", "init")
	write(t, root, "app.txt", "v2\n")
	runGit(t, root, "add", "app.txt") // staged scope for commit gates
	write(t, root, "ytif-inventory.yaml", `version: 2
checks:
  - runner: command
    unit: lint
    name: gofmt
    placement: commit
    tags: [mistake, silent, manual]
    accident: A commit leaves a file unformatted.
    detection: Runs gofmt -l over the lint unit and fails on any listed file.
    impact: Later diffs carry formatting noise.
    delete_when: Formatting is enforced by the editor on save.
  - runner: vitest-test
    unit: app/x.test.ts
    name: renders
    placement: commit
    tags: [mistake, silent, manual]
    accident: A commit breaks the component render.
    detection: Renders the component in vitest and fails on output mismatch.
    impact: The page renders wrong content.
    delete_when: The component no longer exists.
`)
	f := &fixture{root: root, bin: testhelp.FakeAdapter(t)}
	write(t, root, "ytif-execution.yaml", `version: 1
adapter:
  command: [`+f.bin+`]
  runners: [command, vitest-test]
profiles:
  node:
    description: Node checks for hooks.
`)
	return f
}

func head(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func writeReplyFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "reply.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) stageReply(t *testing.T, reply string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "reply.json")
	if err := os.WriteFile(path, []byte(reply), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("YTIF_FAKE_RESPONSE", path)
	for _, v := range []string{"YTIF_FAKE_DISCOVER_RESPONSE", "YTIF_FAKE_SELECT_RESPONSE", "YTIF_FAKE_RUN_RESPONSE",
		"YTIF_FAKE_EXIT", "YTIF_FAKE_DISCOVER_EXIT", "YTIF_FAKE_SELECT_EXIT", "YTIF_FAKE_RUN_EXIT"} {
		t.Setenv(v, "")
	}
	t.Setenv("YTIF_FAKE_REQUEST_LOG", "")
}

const allPass = `{"selected":[
	{"runner":"command","unit":"lint","name":"gofmt"},
	{"runner":"vitest-test","unit":"app/x.test.ts","name":"renders"}],
	"results":[
	{"runner":"command","unit":"lint","name":"gofmt","outcome":"pass","elapsedMs":3},
	{"runner":"vitest-test","unit":"app/x.test.ts","name":"renders","outcome":"pass"}],
	"invocations":[{"runner":"command","unit":"lint","what":"run","elapsedMs":9}]}`

func (f *fixture) runProfile(t *testing.T, ctx context.Context, g string, config gate.ConfigChecker) (int, string, string, []map[string]any) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	records := filepath.Join(t.TempDir(), "records.jsonl")
	exit := gate.Run(ctx, f.root, gate.Options{
		Gate: g, Profile: "node", Records: records,
		Stdout: &stdout, Stderr: &stderr, Config: config,
	})
	data, err := os.ReadFile(records)
	if err != nil {
		if os.IsNotExist(err) {
			return exit, stdout.String(), stderr.String(), nil
		}
		t.Fatal(err)
	}
	var lines []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, m)
	}
	return exit, stdout.String(), stderr.String(), lines
}

func resultLines(lines []map[string]any) []map[string]any {
	var out []map[string]any
	for _, l := range lines {
		if l["kind"] == "result" {
			out = append(out, l)
		}
	}
	return out
}

// TestProfileGate runs profile gates against real git fixtures and a real
// adapter subprocess: outcomes map to passes, failures, skips, and
// dispatch errors faithfully, and selection is reconciled without native
// discovery.
func TestProfileGate(t *testing.T) {
	ctx := context.Background()

	t.Run("pass records every outcome", func(t *testing.T) {
		f := newFixture(t)
		f.stageReply(t, allPass)
		exit, stdout, _, lines := f.runProfile(t, ctx, "commit", gateconf.Checker{})
		if exit != 0 {
			t.Fatalf("exit = %d\n%s", exit, stdout)
		}
		for _, want := range []string{"command:lint:gofmt", "vitest-test:app/x.test.ts:renders", "2 passed"} {
			if !strings.Contains(stdout, want) {
				t.Errorf("stdout lacks %q\n%s", want, stdout)
			}
		}
		results := resultLines(lines)
		if len(results) != 2 || results[0]["outcome"] != "pass" || results[0]["gate"] != "commit" {
			t.Fatalf("records = %v", lines)
		}
		wantCommit := head(t, f.root)
		for _, r := range results {
			if r["commit"] != wantCommit || r["worktree"] != f.root || r["profile"] != "node" {
				t.Fatalf("provenance lost: %v", r)
			}
		}
	})

	t.Run("cached result remains cached without fresh timing", func(t *testing.T) {
		f := newFixture(t)
		f.stageReply(t, `{"selected":[{"runner":"command","unit":"lint","name":"gofmt"}],"results":[{"runner":"command","unit":"lint","name":"gofmt","outcome":"cached","elapsedMs":9,"output":"Nx reused task cache"}]}`)
		exit, stdout, _, lines := f.runProfile(t, ctx, "commit", nil)
		if exit != 0 || !strings.Contains(stdout, "1 cached") || !strings.Contains(stdout, "0 passed") {
			t.Fatalf("cached replay rejected or presented as fresh: exit=%d\n%s", exit, stdout)
		}
		results := resultLines(lines)
		if len(results) != 1 || results[0]["outcome"] != "cached" || results[0]["detail"] != "Nx reused task cache" {
			t.Fatalf("cache evidence lost: %v", lines)
		}
		if _, ok := results[0]["elapsed_ms"]; ok {
			t.Fatalf("cache replay has fresh test timing: %v", results[0])
		}
	})

	t.Run("blocked-only results cannot certify the gate", func(t *testing.T) {
		f := newFixture(t)
		f.stageReply(t, `{"selected":[{"runner":"command","unit":"lint","name":"gofmt"}],"results":[{"runner":"command","unit":"lint","name":"gofmt","outcome":"blocked","output":"dependency unavailable"}]}`)
		exit, stdout, _, lines := f.runProfile(t, ctx, "commit", nil)
		if exit != 1 || !strings.Contains(stdout, "1 blocked") {
			t.Fatalf("blocked selection certified: exit=%d\n%s", exit, stdout)
		}
		if results := resultLines(lines); len(results) != 1 || results[0]["outcome"] != "blocked" {
			t.Fatalf("blocker lost: %v", lines)
		}
	})

	t.Run("fail fails the gate", func(t *testing.T) {
		f := newFixture(t)
		f.stageReply(t, `{"selected":[{"runner":"command","unit":"lint","name":"gofmt"}],
			"results":[{"runner":"command","unit":"lint","name":"gofmt","outcome":"fail","output":"bad\n"}]}`)
		exit, stdout, _, lines := f.runProfile(t, ctx, "commit", nil)
		if exit != 1 {
			t.Fatalf("exit = %d\n%s", exit, stdout)
		}
		if !strings.Contains(stdout, "command:lint:gofmt") || !strings.Contains(stdout, "bad") || !strings.Contains(stdout, "1 failed") {
			t.Errorf("stdout lacks the failure\n%s", stdout)
		}
		if results := resultLines(lines); len(results) != 1 || results[0]["outcome"] != "fail" || results[0]["detail"] != "bad\n" {
			t.Fatalf("records = %v", lines)
		}
	})

	t.Run("missing outcome is a dispatch error", func(t *testing.T) {
		f := newFixture(t)
		f.stageReply(t, `{"selected":[
			{"runner":"command","unit":"lint","name":"gofmt"},
			{"runner":"vitest-test","unit":"app/x.test.ts","name":"renders"}],
			"results":[{"runner":"command","unit":"lint","name":"gofmt","outcome":"pass"}]}`)
		exit, stdout, _, _ := f.runProfile(t, ctx, "commit", nil)
		if exit != 1 || !strings.Contains(stdout, "vitest-test:app/x.test.ts:renders") ||
			!strings.Contains(stdout, "no outcome reported") {
			t.Fatalf("exit = %d\n%s", exit, stdout)
		}
	})

	t.Run("skip stays a skip", func(t *testing.T) {
		f := newFixture(t)
		f.stageReply(t, `{"selected":[{"runner":"command","unit":"lint","name":"gofmt"}],
			"results":[],
			"skipped":[{"runner":"command","unit":"lint","name":"gofmt"}]}`)
		exit, stdout, _, lines := f.runProfile(t, ctx, "commit", nil)
		if exit != 0 || !strings.Contains(stdout, "command:lint:gofmt") || !strings.Contains(stdout, "1 skipped") {
			t.Fatalf("exit = %d\n%s", exit, stdout)
		}
		results := resultLines(lines)
		if len(results) != 1 || results[0]["outcome"] != "skip" || results[0]["key"] != "command:lint:gofmt" {
			t.Fatalf("a skip must be preserved as a skip record: %v", lines)
		}
		if _, ok := results[0]["elapsed_ms"]; ok {
			t.Fatalf("a skip must carry no timing: %v", results[0])
		}
	})

	t.Run("unknown outcome fails with its reason", func(t *testing.T) {
		f := newFixture(t)
		f.stageReply(t, `{"selected":[{"runner":"command","unit":"lint","name":"gofmt"}],
			"results":[{"runner":"command","unit":"lint","name":"gofmt","outcome":"passed"}]}`)
		exit, stdout, _, _ := f.runProfile(t, ctx, "commit", nil)
		if exit != 1 || !strings.Contains(stdout, "unknown outcome") {
			t.Fatalf("exit = %d\n%s", exit, stdout)
		}
	})

	t.Run("unselected report fails", func(t *testing.T) {
		f := newFixture(t)
		f.stageReply(t, `{"selected":[{"runner":"command","unit":"lint","name":"gofmt"}],
			"results":[
			{"runner":"command","unit":"lint","name":"gofmt","outcome":"pass"},
			{"runner":"vitest-test","unit":"app/x.test.ts","name":"renders","outcome":"pass"}]}`)
		exit, stdout, _, _ := f.runProfile(t, ctx, "commit", nil)
		if exit != 1 || !strings.Contains(stdout, "did not select") {
			t.Fatalf("exit = %d\n%s", exit, stdout)
		}
	})

	t.Run("unregistered selection is a finding", func(t *testing.T) {
		f := newFixture(t)
		f.stageReply(t, `{"selected":[{"runner":"command","unit":"lint","name":"stray"}],
			"results":[{"runner":"command","unit":"lint","name":"stray","outcome":"pass"}]}`)
		exit, stdout, _, _ := f.runProfile(t, ctx, "commit", nil)
		if exit != 1 || !strings.Contains(stdout, "unregistered command:lint:stray") {
			t.Fatalf("exit = %d\n%s", exit, stdout)
		}
	})

	t.Run("later placement fails before running", func(t *testing.T) {
		f := newFixture(t)
		write(t, f.root, "slow.txt", "x\n")
		runGit(t, f.root, "add", "slow.txt")
		inv, err := os.ReadFile(filepath.Join(f.root, "ytif-inventory.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		slow := strings.Replace(string(inv), "placement: commit", "placement: ci", 1)
		write(t, f.root, "ytif-inventory.yaml", slow)
		// The select reply approves the misplaced check; the run reply
		// is garbage, so any run attempt would fail parsing instead.
		f.stageReply(t, `{"selected":[{"runner":"command","unit":"lint","name":"gofmt"}]}`)
		t.Setenv("YTIF_FAKE_RUN_RESPONSE", writeReplyFile(t, `not json`))
		exit, stdout, _, lines := f.runProfile(t, ctx, "commit", nil)
		if exit != 1 || !strings.Contains(stdout, "placed at ci") {
			t.Fatalf("exit = %d\n%s", exit, stdout)
		}
		if strings.Contains(stdout, "read run reply") {
			t.Fatalf("the run must never start after a preflight failure:\n%s", stdout)
		}
		if len(resultLines(lines)) != 0 {
			t.Fatalf("no outcome may be recorded without a run: %v", lines)
		}
	})

	t.Run("profile reports native runner keys", func(t *testing.T) {
		f := newFixture(t)
		inv, err := os.ReadFile(filepath.Join(f.root, "ytif-inventory.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		write(t, f.root, "ytif-inventory.yaml", string(inv)+`  - runner: node-test
    unit: tools/check.test.mjs
    name: works
    placement: commit
    tags: [mistake, silent, manual]
    accident: A commit breaks the tool check.
    detection: Executes the tool check file and fails on assertion error.
    impact: The tool silently misbehaves.
    delete_when: The tool no longer exists.
`)
		f.stageReply(t, `{"selected":[{"runner":"node-test","unit":"tools/check.test.mjs","name":"works"}],
			"results":[{"runner":"node-test","unit":"tools/check.test.mjs","name":"works","outcome":"pass"}]}`)
		exit, stdout, _, _ := f.runProfile(t, ctx, "commit", nil)
		if exit != 0 || !strings.Contains(stdout, "node-test:tools/check.test.mjs:works") {
			t.Fatalf("exit = %d\n%s", exit, stdout)
		}
	})

	t.Run("detection is required", func(t *testing.T) {
		f := newFixture(t)
		inv, err := os.ReadFile(filepath.Join(f.root, "ytif-inventory.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		stripped := strings.Replace(string(inv), "    detection: Runs gofmt -l over the lint unit and fails on any listed file.\n", "", 1)
		if stripped == string(inv) {
			t.Fatal("fixture changed; update the stripped detection line")
		}
		write(t, f.root, "ytif-inventory.yaml", stripped)
		f.stageReply(t, allPass)
		exit, stdout, _, _ := f.runProfile(t, ctx, "commit", nil)
		if exit != 1 || !strings.Contains(stdout, "missing-contract command:lint:gofmt: empty detection") {
			t.Fatalf("exit = %d\n%s", exit, stdout)
		}
	})

	t.Run("process failure keeps partial results", func(t *testing.T) {
		f := newFixture(t)
		f.stageReply(t, `{"selected":[
			{"runner":"command","unit":"lint","name":"gofmt"},
			{"runner":"vitest-test","unit":"app/x.test.ts","name":"renders"}],
			"results":[{"runner":"command","unit":"lint","name":"gofmt","outcome":"pass"}]}`)
		t.Setenv("YTIF_FAKE_EXIT", "1")
		t.Setenv("YTIF_FAKE_SELECT_EXIT", "0")
		exit, stdout, _, lines := f.runProfile(t, ctx, "commit", nil)
		if exit != 1 {
			t.Fatalf("exit = %d\n%s", exit, stdout)
		}
		if results := resultLines(lines); len(results) != 1 || results[0]["key"] != "command:lint:gofmt" {
			t.Fatalf("partial results lost: %v", lines)
		}
	})

	t.Run("garbage reply fails", func(t *testing.T) {
		f := newFixture(t)
		f.stageReply(t, `not json`)
		exit, stdout, _, _ := f.runProfile(t, ctx, "commit", nil)
		if exit != 1 || !strings.Contains(stdout, "read select reply") {
			t.Fatalf("exit = %d\n%s", exit, stdout)
		}
	})

	t.Run("gate configuration still reconciles", func(t *testing.T) {
		f := newFixture(t)
		write(t, f.root, ".github/workflows/ci.yml", "name: ci\non: push\njobs:\n  x:\n    runs-on: ubuntu-latest\n    steps:\n      - run: go vet ./...\n")
		runGit(t, f.root, "add", ".github/workflows/ci.yml")
		f.stageReply(t, allPass)
		exit, stdout, _, _ := f.runProfile(t, ctx, "commit", gateconf.Checker{})
		if exit != 1 || !strings.Contains(stdout, "gate-config") {
			t.Fatalf("exit = %d\n%s", exit, stdout)
		}
	})

	t.Run("unknown profile is usage", func(t *testing.T) {
		f := newFixture(t)
		var stdout, stderr bytes.Buffer
		exit := gate.Run(ctx, f.root, gate.Options{
			Gate: "commit", Profile: "nope", Records: filepath.Join(t.TempDir(), "r.jsonl"),
			Stdout: &stdout, Stderr: &stderr,
		})
		if exit != 2 || !strings.Contains(stderr.String(), "unknown profile") {
			t.Fatalf("exit = %d\n%s", exit, stderr.String())
		}
	})

	t.Run("missing execution list is usage", func(t *testing.T) {
		f := newFixture(t)
		if err := os.Remove(filepath.Join(f.root, "ytif-execution.yaml")); err != nil {
			t.Fatal(err)
		}
		var stdout, stderr bytes.Buffer
		exit := gate.Run(ctx, f.root, gate.Options{
			Gate: "commit", Profile: "node", Records: filepath.Join(t.TempDir(), "r.jsonl"),
			Stdout: &stdout, Stderr: &stderr,
		})
		if exit != 2 || !strings.Contains(stderr.String(), "ytif-execution.yaml") {
			t.Fatalf("exit = %d\n%s", exit, stderr.String())
		}
	})

	t.Run("profile needs no native SDKs", func(t *testing.T) {
		f := newFixture(t)
		f.stageReply(t, allPass)
		git, err := exec.LookPath("git")
		if err != nil {
			t.Fatal(err)
		}
		bin := t.TempDir()
		if err := os.Symlink(git, filepath.Join(bin, "git")); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", bin)
		if _, err := exec.LookPath("dotnet"); err == nil {
			t.Fatal("dotnet on PATH would not prove the profile avoids it")
		}
		exit, stdout, _, _ := f.runProfile(t, ctx, "commit", nil)
		if exit != 0 {
			t.Fatalf("exit = %d\n%s", exit, stdout)
		}
	})

	t.Run("full gate runs adapter checks", func(t *testing.T) {
		f := newFixture(t)
		// No native tripwire here: the native sources must discover
		// nothing so the adapter's execution decides the gate.
		if err := os.Remove(filepath.Join(f.root, "ok_test.go")); err != nil {
			t.Fatal(err)
		}
		// One reply file answers both discovery and selection; each
		// operation's parser ignores the other's fields.
		disc := filepath.Join(t.TempDir(), "discover.json")
		write(t, filepath.Dir(disc), "discover.json", `{"checks":[
			{"runner":"command","unit":"lint","name":"gofmt"},
			{"runner":"vitest-test","unit":"app/x.test.ts","name":"renders"}],
			"selected":[
			{"runner":"command","unit":"lint","name":"gofmt"},
			{"runner":"vitest-test","unit":"app/x.test.ts","name":"renders"}]}`)
		run := filepath.Join(t.TempDir(), "run.json")
		write(t, filepath.Dir(run), "run.json", allPass)
		t.Setenv("YTIF_FAKE_RESPONSE", disc)
		t.Setenv("YTIF_FAKE_RUN_RESPONSE", run)
		var stdout, stderr bytes.Buffer
		exit := gate.Run(ctx, f.root, gate.Options{
			Gate: "commit", Records: filepath.Join(t.TempDir(), "r.jsonl"),
			Stdout: &stdout, Stderr: &stderr,
			Sources: []check.Source{
				&gohost.Source{},
				&gotest.Source{},
				&jstest.Source{Runner: jstest.Bun},
				&jstest.Source{Runner: jstest.Node},
				&dotnet.Source{},
				adapter.New([]string{f.bin}, []string{"command", "vitest-test"}, ""),
			},
			Config: gateconf.Checker{},
		})
		if exit != 0 || !strings.Contains(stdout.String(), "2 passed") {
			t.Fatalf("exit = %d\n%s\n%s", exit, stdout.String(), stderr.String())
		}
	})

	t.Run("full gate needs the native toolchain", func(t *testing.T) {
		f := newFixture(t)
		f.stageReply(t, `{"checks":[
			{"runner":"command","unit":"lint","name":"gofmt"},
			{"runner":"vitest-test","unit":"app/x.test.ts","name":"renders"}]}`)
		git, err := exec.LookPath("git")
		if err != nil {
			t.Fatal(err)
		}
		bin := t.TempDir()
		if err := os.Symlink(git, filepath.Join(bin, "git")); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", bin)
		var stdout, stderr bytes.Buffer
		exit := gate.Run(ctx, f.root, gate.Options{
			Gate: "commit", Records: filepath.Join(t.TempDir(), "r.jsonl"),
			Stdout: &stdout, Stderr: &stderr,
			Sources: []check.Source{
				&gohost.Source{},
				&gotest.Source{},
				&jstest.Source{Runner: jstest.Bun},
				&jstest.Source{Runner: jstest.Node},
				&dotnet.Source{},
				adapter.New([]string{f.bin}, []string{"command", "vitest-test"}, ""),
			},
			Config: nil,
		})
		if exit != 1 || !strings.Contains(stdout.String(), "discovery-failed") {
			t.Fatalf("exit = %d\n%s\n%s", exit, stdout.String(), stderr.String())
		}
	})
}

// TestListMachine covers the machine-readable list and the runner scope:
// JSON carries contracts and structured findings, and --runners limits the
// world without touching excluded runners.
func TestListMachine(t *testing.T) {
	ctx := context.Background()

	sources := func(f *fixture) []check.Source {
		return []check.Source{adapter.New([]string{f.bin}, []string{"command", "vitest-test"}, "")}
	}
	list := func(t *testing.T, f *fixture, opts gate.ListOptions) (int, string) {
		t.Helper()
		var stdout, stderr bytes.Buffer
		exit := gate.List(ctx, f.root, sources(f), nil, &stdout, &stderr, opts)
		if stderr.String() != "" {
			t.Fatalf("stderr = %q", stderr.String())
		}
		return exit, stdout.String()
	}

	t.Run("json carries contracts and findings", func(t *testing.T) {
		f := newFixture(t)
		f.stageReply(t, `{"checks":[
			{"runner":"command","unit":"lint","name":"gofmt"},
			{"runner":"vitest-test","unit":"app/x.test.ts","name":"renders"},
			{"runner":"command","unit":"lint","name":"stray"}]}`)
		exit, stdout := list(t, f, gate.ListOptions{JSON: true})
		if exit != 1 {
			t.Fatalf("exit = %d\n%s", exit, stdout)
		}
		var doc struct {
			Managed []struct {
				Runner     string `json:"runner"`
				Unit       string `json:"unit"`
				Name       string `json:"name"`
				Placement  string `json:"placement"`
				Accident   string `json:"accident"`
				DeleteWhen string `json:"delete_when"`
			} `json:"managed"`
			Discovered map[string]struct {
				Checks     int  `json:"checks"`
				Incomplete bool `json:"incomplete"`
			} `json:"discovered"`
			Findings []struct {
				Kind   string `json:"kind"`
				Key    string `json:"key"`
				Detail string `json:"detail"`
			} `json:"findings"`
		}
		if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
			t.Fatalf("%v\n%s", err, stdout)
		}
		if len(doc.Managed) != 2 || doc.Managed[0].Accident == "" || doc.Managed[0].DeleteWhen == "" {
			t.Fatalf("managed = %+v", doc.Managed)
		}
		if doc.Discovered["command"].Checks != 2 || doc.Discovered["vitest-test"].Checks != 1 {
			t.Fatalf("discovered = %+v", doc.Discovered)
		}
		if len(doc.Findings) != 1 || doc.Findings[0].Kind != "unregistered" || doc.Findings[0].Key != "command:lint:stray" {
			t.Fatalf("findings = %+v", doc.Findings)
		}
	})

	t.Run("clean json passes", func(t *testing.T) {
		f := newFixture(t)
		f.stageReply(t, `{"checks":[
			{"runner":"command","unit":"lint","name":"gofmt"},
			{"runner":"vitest-test","unit":"app/x.test.ts","name":"renders"}]}`)
		exit, stdout := list(t, f, gate.ListOptions{JSON: true})
		if exit != 0 {
			t.Fatalf("exit = %d\n%s", exit, stdout)
		}
		var doc struct {
			Managed  []any `json:"managed"`
			Findings []any `json:"findings"`
		}
		if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
			t.Fatal(err)
		}
		if len(doc.Managed) != 2 || len(doc.Findings) != 0 {
			t.Fatalf("doc = %s", stdout)
		}
	})

	t.Run("runners scope the world", func(t *testing.T) {
		f := newFixture(t)
		f.stageReply(t, `{"checks":[
			{"runner":"command","unit":"lint","name":"gofmt"},
			{"runner":"vitest-test","unit":"app/x.test.ts","name":"renders"}]}`)
		exit, stdout := list(t, f, gate.ListOptions{JSON: true, Runners: []string{"command"}})
		if exit != 0 {
			t.Fatalf("exit = %d\n%s", exit, stdout)
		}
		var doc struct {
			Managed []struct {
				Runner string `json:"runner"`
			} `json:"managed"`
			Discovered map[string]struct {
				Checks int `json:"checks"`
			} `json:"discovered"`
		}
		if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
			t.Fatal(err)
		}
		if len(doc.Managed) != 1 || doc.Managed[0].Runner != "command" {
			t.Fatalf("managed = %+v", doc.Managed)
		}
		if len(doc.Discovered) != 1 || doc.Discovered["command"].Checks != 1 {
			t.Fatalf("discovered = %+v", doc.Discovered)
		}
	})
}
