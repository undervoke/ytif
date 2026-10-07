package adapter_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/undervoke/ytif/internal/adapter"
	"github.com/undervoke/ytif/internal/check"
	"github.com/undervoke/ytif/internal/inventory"
	"github.com/undervoke/ytif/internal/testhelp"
)

// TestAdapterProtocol drives the adapter source against the fake adapter
// subprocess: discovery and run replies map to keys and reports exactly,
// violations fail with reasons, and process failures keep partial results.
func TestAdapterProtocol(t *testing.T) {
	bin := testhelp.FakeAdapter(t)
	ctx := context.Background()

	newSource := func(t *testing.T, profile string) (*adapter.Source, check.Repo, *bytes.Buffer) {
		t.Helper()
		var log bytes.Buffer
		repo := check.Repo{Root: t.TempDir(), Tracked: []string{"a.ts"}, Log: &log}
		return adapter.New([]string{bin}, []string{"command", "vitest-test"}, profile), repo, &log
	}
	stage := func(t *testing.T, reply string) {
		t.Helper()
		t.Setenv("YTIF_FAKE_RESPONSE", writeReply(t, reply))
		for _, v := range []string{"YTIF_FAKE_DISCOVER_RESPONSE", "YTIF_FAKE_SELECT_RESPONSE", "YTIF_FAKE_RUN_RESPONSE",
			"YTIF_FAKE_EXIT", "YTIF_FAKE_DISCOVER_EXIT", "YTIF_FAKE_SELECT_EXIT", "YTIF_FAKE_RUN_EXIT"} {
			t.Setenv(v, "")
		}
		t.Setenv("YTIF_FAKE_REQUEST_LOG", "")
	}
	selectOnly := func(t *testing.T, reply string) {
		t.Helper()
		t.Setenv("YTIF_FAKE_SELECT_RESPONSE", writeReply(t, reply))
	}

	t.Run("discover maps checks and sends the request", func(t *testing.T) {
		src, repo, _ := newSource(t, "")
		log := filepath.Join(t.TempDir(), "request.json")
		t.Setenv("YTIF_FAKE_RESPONSE", writeReply(t, `{"checks":[
			{"runner":"command","unit":"lint","name":"gofmt"},
			{"runner":"vitest-test","unit":"app/x.test.ts","name":"renders"}]}`))
		t.Setenv("YTIF_FAKE_REQUEST_LOG", log)
		keys, invs, err := src.Discover(ctx, repo)
		if err != nil {
			t.Fatal(err)
		}
		if len(invs) != 0 || len(keys) != 2 || keys[0].Runner != "command" || keys[1].Name != "renders" {
			t.Fatalf("unexpected discovery: %v %v", keys, invs)
		}
		raw := read(t, log)
		if !strings.Contains(string(raw), `"operation":"discover"`) {
			t.Fatalf("request is not lowercase wire JSON: %s", raw)
		}
		var req struct {
			Version   int      `json:"version"`
			Operation string   `json:"operation"`
			Root      string   `json:"root"`
			Tracked   []string `json:"tracked"`
			Profile   string   `json:"profile"`
		}
		if err := json.Unmarshal(raw, &req); err != nil {
			t.Fatal(err)
		}
		if req.Version != 1 || req.Operation != "discover" || req.Root != repo.Root || req.Profile != "" || len(req.Tracked) != 1 {
			t.Fatalf("unexpected request: %+v", req)
		}
	})

	t.Run("discover keeps valid checks past violations", func(t *testing.T) {
		src, repo, _ := newSource(t, "")
		stage(t, `{"checks":[
			{"runner":"command","unit":"lint","name":"gofmt"},
			{"runner":"command","unit":"lint","name":""},
			{"runner":"node-test","unit":"a.test.mjs","name":"x"}]}`)
		keys, _, err := src.Discover(ctx, repo)
		if err == nil || len(keys) != 1 {
			t.Fatalf("keys = %v, err = %v; want 1 key and an error", keys, err)
		}
		if msg := err.Error(); !strings.Contains(msg, "node-test") || !strings.Contains(msg, "declared runners") {
			t.Fatalf("error %q does not explain the foreign runner", msg)
		}
	})

	t.Run("discover rejects garbage and missing checks", func(t *testing.T) {
		src, repo, _ := newSource(t, "")
		for _, reply := range []string{`not json`, `{"results":[]}`} {
			stage(t, reply)
			if keys, _, err := src.Discover(ctx, repo); err == nil || keys != nil {
				t.Fatalf("reply %q: keys = %v, err = %v; want an error", reply, keys, err)
			}
		}
	})

	t.Run("run maps outcomes, skips, and invocations", func(t *testing.T) {
		src, repo, _ := newSource(t, "")
		log := filepath.Join(t.TempDir(), "request.json")
		stage(t, `{"selected":[
			{"runner":"command","unit":"lint","name":"gofmt"},
			{"runner":"command","unit":"lint","name":"vuln"},
			{"runner":"vitest-test","unit":"app/x.test.ts","name":"renders"},
			{"runner":"vitest-test","unit":"app/x.test.ts","name":"skipped"}],
			"results":[
			{"runner":"command","unit":"lint","name":"gofmt","outcome":"pass","elapsedMs":12},
			{"runner":"command","unit":"lint","name":"vuln","outcome":"fail","output":"CVE-1\n"},
			{"runner":"vitest-test","unit":"app/x.test.ts","name":"renders","outcome":"blocked"}],
			"skipped":[{"runner":"vitest-test","unit":"app/x.test.ts","name":"skipped"}],
			"invocations":[{"runner":"command","unit":"lint","what":"run","elapsedMs":30},
			{"runner":"vitest-test","unit":"app/x.test.ts","what":"run","error":"boom"}]}`)
		t.Setenv("YTIF_FAKE_REQUEST_LOG", log)
		selected, rep, err := src.RunSelected(ctx, repo, check.Input{Gate: "ci", Scope: "tree", Files: []string{"a.ts"}})
		if err != nil {
			t.Fatal(err)
		}
		if len(selected) != 4 {
			t.Fatalf("selected = %v", selected)
		}
		outcomes := map[string]check.Outcome{}
		for _, r := range rep.Results {
			outcomes[r.Key.Name] = r.Outcome
			if r.Key.Name == "gofmt" && (!r.Timed || r.Elapsed.Milliseconds() != 12) {
				t.Fatalf("gofmt timing = %v timed=%v", r.Elapsed, r.Timed)
			}
			if r.Key.Name == "vuln" && (r.Outcome != check.Fail || r.Output != "CVE-1\n") {
				t.Fatalf("vuln result = %+v", r)
			}
		}
		if outcomes["gofmt"] != check.Pass || outcomes["vuln"] != check.Fail || outcomes["renders"] != check.Blocked {
			t.Fatalf("outcomes = %v", outcomes)
		}
		if len(rep.Skipped) != 1 || rep.Skipped[0].Name != "skipped" {
			t.Fatalf("skipped = %v", rep.Skipped)
		}
		if len(rep.Invocations) != 2 || rep.Invocations[1].Err == nil {
			t.Fatalf("invocations = %+v", rep.Invocations)
		}
		var req struct {
			Operation string `json:"operation"`
			Profile   string `json:"profile"`
			Gate      string `json:"gate"`
			Scope     string `json:"scope"`
			Selected  []struct {
				Runner string `json:"runner"`
				Unit   string `json:"unit"`
				Name   string `json:"name"`
			} `json:"selected"`
		}
		if err := json.Unmarshal(read(t, log), &req); err != nil {
			t.Fatal(err)
		}
		if req.Operation != "run" || req.Gate != "ci" || req.Scope != "tree" || len(req.Selected) != 4 {
			t.Fatalf("unexpected run request: %+v", req)
		}
	})

	t.Run("select validates the selection", func(t *testing.T) {
		src, repo, _ := newSource(t, "")
		for name, reply := range map[string]string{
			"missing selected": `{"results":[]}`,
			"garbage":          `not json`,
			"unknown runner":   `{"selected":[{"runner":"bogus","unit":"u","name":"n"}]}`,
			"selected twice":   `{"selected":[{"runner":"command","unit":"u","name":"n"},{"runner":"command","unit":"u","name":"n"}]}`,
			"empty name":       `{"selected":[{"runner":"command","unit":"u","name":""}]}`,
		} {
			t.Run(name, func(t *testing.T) {
				selectOnly(t, reply)
				t.Setenv("YTIF_FAKE_RESPONSE", writeReply(t, `{"selected":[]}`))
				if _, _, err := src.Select(ctx, repo, check.Input{Gate: "ci"}); err == nil {
					t.Fatal("want the select violation reported")
				}
			})
		}
	})

	t.Run("preflight holds back later placement", func(t *testing.T) {
		inv := inventory.Inventory{Version: 2, Checks: []inventory.Entry{
			{Runner: "command", Unit: "lint", Name: "fast", Placement: "commit"},
			{Runner: "command", Unit: "lint", Name: "slow", Placement: "ci"},
			{Runner: "command", Unit: "lint", Name: "broken", Placement: "hourly"},
		}}
		fast := check.Key{Runner: "command", Unit: "lint", Name: "fast"}
		slow := check.Key{Runner: "command", Unit: "lint", Name: "slow"}
		broken := check.Key{Runner: "command", Unit: "lint", Name: "broken"}
		stray := check.Key{Runner: "command", Unit: "lint", Name: "stray"}
		for _, c := range []struct {
			name     string
			gate     string
			selected []check.Key
			wantErr  string
		}{
			{"commit runs commit checks", "commit", []check.Key{fast}, ""},
			{"ci runs everything placed", "ci", []check.Key{fast, slow}, ""},
			{"commit holds back ci checks", "commit", []check.Key{fast, slow}, "placed at ci"},
			{"invalid placement fails", "ci", []check.Key{broken}, "invalid placement"},
			{"uninventoried passes to reconciliation", "commit", []check.Key{stray}, ""},
		} {
			t.Run(c.name, func(t *testing.T) {
				err := adapter.Preflight(c.gate, inv, c.selected)
				if c.wantErr == "" && err != nil {
					t.Fatalf("Preflight = %v; want nil", err)
				}
				if c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)) {
					t.Fatalf("Preflight = %v; want %q", err, c.wantErr)
				}
			})
		}
	})

	t.Run("fractional elapsedMs rounds", func(t *testing.T) {
		src, repo, _ := newSource(t, "")
		selectOnly(t, `{"selected":[
			{"runner":"command","unit":"lint","name":"half-up"},
			{"runner":"command","unit":"lint","name":"half-down"},
			{"runner":"command","unit":"lint","name":"whole"}]}`)
		t.Setenv("YTIF_FAKE_RUN_RESPONSE", writeReply(t, `{"selected":[
			{"runner":"command","unit":"lint","name":"half-up"},
			{"runner":"command","unit":"lint","name":"half-down"},
			{"runner":"command","unit":"lint","name":"whole"}],
			"results":[
			{"runner":"command","unit":"lint","name":"half-up","outcome":"pass","elapsedMs":12.5},
			{"runner":"command","unit":"lint","name":"half-down","outcome":"pass","elapsedMs":12.4},
			{"runner":"command","unit":"lint","name":"whole","outcome":"pass","elapsedMs":12}]}`))
		t.Setenv("YTIF_FAKE_RESPONSE", writeReply(t, `{"selected":[]}`))
		_, rep, err := src.RunSelected(ctx, repo, check.Input{Gate: "ci"})
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]int64{}
		for _, r := range rep.Results {
			if !r.Timed {
				t.Fatalf("%s lost its timing", r.Key.Name)
			}
			got[r.Key.Name] = r.Elapsed.Milliseconds()
		}
		if got["half-up"] != 13 || got["half-down"] != 12 || got["whole"] != 12 {
			t.Fatalf("rounded = %v; want map[half-up:13 half-down:12 whole:12]", got)
		}
	})

	t.Run("bad elapsedMs fails", func(t *testing.T) {
		src, repo, _ := newSource(t, "")
		selectOnly(t, `{"selected":[{"runner":"command","unit":"lint","name":"x"}]}`)
		for _, ms := range []string{`-1`, `"12.5ms"`, `true`} {
			t.Setenv("YTIF_FAKE_RUN_RESPONSE", writeReply(t, `{"selected":[
				{"runner":"command","unit":"lint","name":"x"}],
				"results":[{"runner":"command","unit":"lint","name":"x","outcome":"pass","elapsedMs":`+ms+`}]}`))
			t.Setenv("YTIF_FAKE_RESPONSE", writeReply(t, `{"selected":[]}`))
			if _, _, err := src.RunSelected(ctx, repo, check.Input{Gate: "ci"}); err == nil {
				t.Fatalf("elapsedMs %s: want an error", ms)
			}
		}
	})

	t.Run("run selection mismatch fails", func(t *testing.T) {
		src, repo, _ := newSource(t, "")
		selectOnly(t, `{"selected":[
			{"runner":"command","unit":"lint","name":"a"},
			{"runner":"command","unit":"lint","name":"b"}]}`)
		t.Setenv("YTIF_FAKE_RUN_RESPONSE", writeReply(t, `{"selected":[
			{"runner":"command","unit":"lint","name":"a"},
			{"runner":"command","unit":"lint","name":"c"}],
			"results":[
			{"runner":"command","unit":"lint","name":"a","outcome":"pass"},
			{"runner":"command","unit":"lint","name":"c","outcome":"pass"}]}`))
		t.Setenv("YTIF_FAKE_RESPONSE", writeReply(t, `{"selected":[]}`))
		_, rep, err := src.RunSelected(ctx, repo, check.Input{Gate: "ci"})
		if err == nil || !strings.Contains(err.Error(), "differs from the approved") {
			t.Fatalf("err = %v; want the selection mismatch", err)
		}
		// Both reported outcomes are kept; the mismatch fails the gate.
		if len(rep.Results) != 2 {
			t.Fatalf("reported outcomes lost: %+v", rep.Results)
		}
	})

	t.Run("run rejects reply violations but keeps usable outcomes", func(t *testing.T) {
		src, repo, _ := newSource(t, "")
		selectOnly(t, `{"selected":[
			{"runner":"command","unit":"lint","name":"ok"},
			{"runner":"command","unit":"lint","name":"weird"}]}`)
		t.Setenv("YTIF_FAKE_RUN_RESPONSE", writeReply(t, `{"selected":[
			{"runner":"command","unit":"lint","name":"ok"},
			{"runner":"command","unit":"lint","name":"ok"},
			{"runner":"command","unit":"lint","name":"weird"}],
			"results":[
			{"runner":"command","unit":"lint","name":"ok","outcome":"pass"},
			{"runner":"command","unit":"lint","name":"weird","outcome":"passed"},
			{"runner":"command","unit":"lint","name":"stray","outcome":"fail"},
			{"runner":"bogus","unit":"u","name":"n","outcome":"pass"}]}`))
		t.Setenv("YTIF_FAKE_RESPONSE", writeReply(t, `{"selected":[]}`))
		selected, rep, err := src.RunSelected(ctx, repo, check.Input{Gate: "ci"})
		if err == nil {
			t.Fatal("want violations reported")
		}
		msg := err.Error()
		for _, want := range []string{"twice", "unknown outcome", "did not select", "unknown runner"} {
			if !strings.Contains(msg, want) {
				t.Errorf("error %q lacks %q", msg, want)
			}
		}
		if len(selected) != 2 || len(rep.Results) != 1 || rep.Results[0].Key.Name != "ok" {
			t.Fatalf("selected = %v, results = %+v", selected, rep.Results)
		}
	})

	t.Run("profile run accepts native runner keys", func(t *testing.T) {
		src, repo, _ := newSource(t, "node")
		stage(t, `{"selected":[{"runner":"node-test","unit":"a.test.mjs","name":"works"}],
			"results":[{"runner":"node-test","unit":"a.test.mjs","name":"works","outcome":"pass"}]}`)
		selected, rep, err := src.RunSelected(ctx, repo, check.Input{Gate: "commit"})
		if err != nil || len(selected) != 1 || len(rep.Results) != 1 {
			t.Fatalf("selected = %v, results = %v, err = %v", selected, rep.Results, err)
		}

		full, repo2, _ := newSource(t, "")
		stage(t, `{"selected":[{"runner":"node-test","unit":"a.test.mjs","name":"works"}],
			"results":[{"runner":"node-test","unit":"a.test.mjs","name":"works","outcome":"pass"}]}`)
		if _, _, err := full.RunSelected(ctx, repo2, check.Input{Gate: "commit"}); err == nil {
			t.Fatal("a full run must reject native runner keys")
		}
	})

	t.Run("process failure keeps partial results", func(t *testing.T) {
		src, repo, _ := newSource(t, "")
		stage(t, `{"selected":[{"runner":"command","unit":"lint","name":"ok"}],
			"results":[{"runner":"command","unit":"lint","name":"ok","outcome":"pass"}]}`)
		t.Setenv("YTIF_FAKE_EXIT", "1")
		t.Setenv("YTIF_FAKE_SELECT_EXIT", "0")
		selected, rep, err := src.RunSelected(ctx, repo, check.Input{Gate: "ci"})
		if err == nil || len(selected) != 1 || len(rep.Results) != 1 {
			t.Fatalf("selected = %v, results = %v, err = %v", selected, rep.Results, err)
		}
	})

	t.Run("process failure without a reply fails", func(t *testing.T) {
		src, repo, _ := newSource(t, "")
		missing := filepath.Join(t.TempDir(), "absent.json")
		t.Setenv("YTIF_FAKE_RESPONSE", missing)
		t.Setenv("YTIF_FAKE_EXIT", "1")
		if _, _, err := src.RunSelected(ctx, repo, check.Input{Gate: "ci"}); err == nil {
			t.Fatal("want the process failure reported")
		}
	})

	t.Run("stderr reaches the log", func(t *testing.T) {
		src, repo, log := newSource(t, "")
		stage(t, `{"checks":[]}`)
		t.Setenv("YTIF_FAKE_STDERR", "progress\n")
		if _, _, err := src.Discover(ctx, repo); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(log.String(), "progress") {
			t.Fatalf("log = %q; want adapter progress", log.String())
		}
	})
}

func writeReply(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "reply.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func read(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
