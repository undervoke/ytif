// Package cirecords reads the record artifacts a GitHub repository holds for
// its CI runs. It keeps them in a cache beside the local records, so a build
// shows local and CI results together and an offline or unauthenticated build
// still renders what it already downloaded.
package cirecords

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/undervoke/ytif/internal/gitx"
	"github.com/undervoke/ytif/internal/record"
)

const (
	// DefaultBase is the GitHub REST API root.
	DefaultBase = "https://api.github.com"
	// Host is the only host the sync reads; a remote naming another host
	// leaves the board with local records only.
	Host = "github.com"

	// catalogTTL bounds how long a catalog listing is reused.
	catalogTTL = 5 * time.Minute
	// maxDownloads bounds how many artifacts one build downloads, so a
	// first build over a long history stays a series of ordinary builds.
	maxDownloads = 20
	// perPage is the largest artifact page the API serves.
	perPage = 100
	// requestTimeout ends one request that hangs.
	requestTimeout = 60 * time.Second
	// maxBytes bounds one response body and one extracted record file, so a
	// huge or highly compressed artifact cannot exhaust memory.
	maxBytes = 64 << 20
)

// errTooLarge is a body or record file over maxBytes.
var errTooLarge = fmt.Errorf("larger than %d MiB", maxBytes>>20)

// Status kinds for a sync that stopped before finishing.
const (
	StatusOffline       = "offline"        // a request failed; nothing new was asked for
	StatusNoCredentials = "no credentials" // no token to reach the API
	StatusNoArtifact    = "no artifact"    // the repository holds no record artifact
)

// Repo is one GitHub repository.
type Repo struct {
	Owner string
	Name  string
}

func (r Repo) String() string { return r.Owner + "/" + r.Name }

// Status says where the CI records came from: the repository, when its
// catalog last refreshed, how many artifacts still wait, and why the last
// sync stopped early.
type Status struct {
	Repo    string    // owner/name
	Synced  time.Time // last catalog refresh; zero when never
	Pending int       // artifacts catalogued but not downloaded
	Kind    string    // "" when the sync finished
}

// Result is what one sync leaves the board: the cached record lines, each CI
// attempt's branch, and the sync status.
type Result struct {
	Lines  []record.Line
	Branch map[string]string // attempt → workflow_run head branch
	Status Status
}

// Client fetches from the GitHub API. The zero value reads the public API
// anonymously.
type Client struct {
	Base  string       // API root; empty means DefaultBase
	Token string       // bearer token; empty means an anonymous request
	HTTP  *http.Client // nil means a client that ends a request after requestTimeout
}

// CacheDir is where a repository's downloaded records live: beside the local
// records, shared by worktrees.
func CacheDir(commonDir string, repo Repo) string {
	return filepath.Join(commonDir, "ytif", "ci-cache", Host, repo.Owner, repo.Name)
}

// Origin parses the GitHub repository root's origin remote names. It reports
// false when the remote is absent or names another host.
func Origin(root string) (Repo, bool) {
	return ParseRemote(gitx.RemoteURL(root, "origin"))
}

// ParseRemote reads a GitHub repository out of a remote URL in https, ssh, or
// scp form. Other hosts are not GitHub repositories here.
func ParseRemote(raw string) (Repo, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Repo{}, false
	}
	host, p := "", ""
	switch {
	case strings.Contains(raw, "://"):
		u, err := url.Parse(raw)
		if err != nil {
			return Repo{}, false
		}
		host, p = u.Hostname(), u.Path
	case strings.Contains(raw, ":"):
		// scp form: [user@]host:owner/repo.git
		h, rest, _ := strings.Cut(raw, ":")
		host, p = h[strings.LastIndex(h, "@")+1:], rest
	default:
		return Repo{}, false
	}
	if !strings.EqualFold(host, Host) {
		return Repo{}, false
	}
	parts := strings.Split(strings.Trim(strings.TrimSuffix(p, ".git"), "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return Repo{}, false
	}
	return Repo{Owner: parts[0], Name: parts[1]}, true
}

// Token returns the token to send: GH_TOKEN, GITHUB_TOKEN, or the one the gh
// CLI stored. It returns "" when none is available.
func Token() string {
	for _, name := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			return v
		}
	}
	out, err := exec.Command("gh", "auth", "token", "--hostname", Host).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// Sync brings the cache under commonDir up to date for root's origin
// repository and returns every cached record. A sync that cannot finish
// returns the cached records it has and an error saying why.
func (c Client) Sync(root, commonDir string, now time.Time) (Result, error) {
	repo, ok := Origin(root)
	if !ok {
		return Result{}, fmt.Errorf("origin is not a %s repository", Host)
	}
	dir := CacheDir(commonDir, repo)
	st, err := loadState(dir)
	s := &syncer{client: c, repo: repo, dir: dir, st: st, now: now}
	if err != nil {
		s.problems = append(s.problems, err)
	}
	err = s.sync()
	lines, branch := s.read()
	return Result{Lines: lines, Branch: branch, Status: s.status()}, errors.Join(append([]error{err}, s.problems...)...)
}

// syncer is one Sync call: the cache it works on, the failure that stopped it,
// and the problems it went on past.
type syncer struct {
	client   Client
	repo     Repo
	dir      string
	st       state
	now      time.Time
	kind     string
	problems []error
}

func (s *syncer) sync() error {
	if s.client.Token == "" {
		s.kind = StatusNoCredentials
		return errors.New("no credentials: set GH_TOKEN or GITHUB_TOKEN, or run gh auth login")
	}
	if s.st.Refreshed.IsZero() || s.now.Sub(s.st.Refreshed) >= catalogTTL {
		pages, err := s.client.catalog(s.repo, s.st.Pages)
		if err != nil {
			s.kind = kindOf(err)
			return err
		}
		s.st.Pages = pages
		s.st.Refreshed = s.now
		if err := saveState(s.dir, s.st); err != nil {
			return err
		}
	}
	if len(s.rows()) == 0 {
		s.kind = StatusNoArtifact
		return fmt.Errorf("%s holds no %s artifacts", s.repo, record.ArtifactName)
	}
	todo := s.pending()
	if len(todo) > maxDownloads {
		todo = todo[:maxDownloads]
	}
	for _, row := range todo {
		if err := s.fetch(row); err != nil {
			s.kind = kindOf(err)
			return err
		}
	}
	return nil
}

// rows returns every catalogued artifact across the stored pages once. A page
// fetched after an upload can repeat the previous page's last artifact.
func (s *syncer) rows() []artifactRow {
	seen := map[int64]bool{}
	var out []artifactRow
	for _, p := range s.st.Pages {
		for _, r := range p.Rows {
			if !seen[r.ID] {
				seen[r.ID] = true
				out = append(out, r)
			}
		}
	}
	return out
}

// held lists the artifacts whose record file the cache holds, oldest first.
// The files, not the index, say what is cached, so a lost file is downloaded
// again and a lost index leaves every file readable.
func (s *syncer) held() []int64 {
	entries, _ := os.ReadDir(filepath.Join(s.dir, "artifacts"))
	var ids []int64
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".jsonl")
		if !ok {
			continue
		}
		if id, err := strconv.ParseInt(name, 10, 64); err == nil {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// file is where an artifact's record file is cached.
func (s *syncer) file(id int64) string {
	return filepath.Join(s.dir, "artifacts", strconv.FormatInt(id, 10)+".jsonl")
}

// pending lists the catalogued artifacts the cache lacks, newest first. An
// expired artifact is no longer downloadable, and one set aside as unusable
// is not tried again.
func (s *syncer) pending() []artifactRow {
	skip := map[int64]bool{}
	for _, id := range s.held() {
		skip[id] = true
	}
	for _, id := range s.st.Unusable {
		skip[id] = true
	}
	var out []artifactRow
	for _, r := range s.rows() {
		if !r.Expired && !skip[r.ID] {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID > out[j].ID
	})
	return out
}

// fetch downloads one artifact and stores its record file. The file appears
// only once whole, so a torn download is retried by the next build. An
// artifact a retry cannot help, because it is gone, too large, or holds no
// record file, is set aside so older artifacts still arrive.
func (s *syncer) fetch(row artifactRow) error {
	u := fmt.Sprintf("%s/repos/%s/actions/artifacts/%d/zip", s.client.base(), s.repo, row.ID)
	body, _, _, err := s.client.get(u, "")
	if err == nil {
		body, err = extract(body)
	} else if !unusable(err) {
		return err
	}
	if err != nil {
		s.problems = append(s.problems, fmt.Errorf("%s: artifact %d set aside: %w", s.repo, row.ID, err))
		s.st.Unusable = append(s.st.Unusable, row.ID)
		return saveState(s.dir, s.st)
	}
	// the branch outlives the catalog row, which goes once the artifact expires
	if row.Branch != "" {
		if s.st.Branches == nil {
			s.st.Branches = map[int64]string{}
		}
		s.st.Branches[row.ID] = row.Branch
		if err := saveState(s.dir, s.st); err != nil {
			return err
		}
	}
	return writeFile(s.file(row.ID), body)
}

// read returns every cached line and each attempt's CI branch. An attempt two
// artifacts both carry counts once: the first artifact that holds it keeps all
// its lines, and the other's copies are dropped.
func (s *syncer) read() ([]record.Line, map[string]string) {
	branchOf := map[int64]string{}
	for _, r := range s.rows() {
		branchOf[r.ID] = r.Branch
	}
	for id, b := range s.st.Branches {
		branchOf[id] = b
	}
	owner := map[string]int64{} // attempt → the artifact its lines come from
	branch := map[string]string{}
	var out []record.Line
	malformed := 0
	for _, id := range s.held() {
		lines, bad, err := record.Read([]string{s.file(id)})
		if err != nil {
			s.problems = append(s.problems, err)
			continue
		}
		malformed += bad
		for _, l := range lines {
			if l.Attempt != "" {
				o, ok := owner[l.Attempt]
				if ok && o != id {
					continue
				}
				if !ok {
					owner[l.Attempt] = id
					if b := branchOf[id]; b != "" {
						branch[l.Attempt] = b
					}
				}
			}
			out = append(out, l)
		}
	}
	if malformed > 0 {
		s.problems = append(s.problems, fmt.Errorf("skipped %d malformed record lines", malformed))
	}
	return out, branch
}

func (s *syncer) status() Status {
	return Status{Repo: s.repo.String(), Synced: s.st.Refreshed, Pending: len(s.pending()), Kind: s.kind}
}

// catalog refreshes the artifact listing, reusing every page the server still
// reports unchanged. Each page keeps its URL so a later refresh asks the same
// page with its own ETag.
func (c Client) catalog(repo Repo, have []page) ([]page, error) {
	var out []page
	u := fmt.Sprintf("%s/repos/%s/actions/artifacts?name=%s&per_page=%d", c.base(), repo, record.ArtifactName, perPage)
	for i := 0; u != ""; i++ {
		var prev page
		if i < len(have) && have[i].URL == u {
			prev = have[i]
		}
		body, h, notModified, err := c.get(u, prev.ETag)
		if err != nil {
			return nil, err
		}
		if notModified {
			prev.URL = u
			out = append(out, prev)
			u = ""
			if i+1 < len(have) {
				u = have[i+1].URL
			}
			continue
		}
		rows, err := parsePage(body)
		if err != nil {
			return nil, fmt.Errorf("%s: catalog: %w", repo, err)
		}
		out = append(out, page{URL: u, ETag: h.Get("ETag"), Rows: rows})
		u = linkNext(h.Get("Link"))
	}
	return out, nil
}

// base is the API root with no trailing slash.
func (c Client) base() string {
	if c.Base == "" {
		return DefaultBase
	}
	return strings.TrimSuffix(c.Base, "/")
}

// get performs one API request. A conditional request the server answers with
// 304 returns notModified and no body.
func (c Client) get(u, etag string) (body []byte, h http.Header, notModified bool, err error) {
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, nil, false, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: requestTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		// a failed redirect names the signed storage URL it followed; naming the
		// API URL instead keeps that download credential out of the message
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, nil, false, fmt.Errorf("%s: %w", u, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		return nil, resp.Header, true, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, nil, false, &httpError{Status: resp.StatusCode, URL: u}
	}
	body, err = readLimited(resp.Body)
	if err != nil {
		return nil, nil, false, fmt.Errorf("%s: %w", u, err)
	}
	return body, resp.Header, false, nil
}

// readLimited reads r whole, failing with errTooLarge past maxBytes.
func readLimited(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err == nil && len(data) > maxBytes {
		return nil, errTooLarge
	}
	return data, err
}

// httpError is a request the API answered with a status the sync cannot use.
type httpError struct {
	Status int
	URL    string
}

func (e *httpError) Error() string {
	return fmt.Sprintf("%s: %d %s", e.URL, e.Status, http.StatusText(e.Status))
}

// kindOf maps a failure to the status kind the page names.
func kindOf(err error) string {
	var he *httpError
	if errors.As(err, &he) && (he.Status == http.StatusUnauthorized || he.Status == http.StatusForbidden) {
		return StatusNoCredentials
	}
	return StatusOffline
}

// unusable reports a download failure a retry cannot fix: the artifact is gone
// or too large.
func unusable(err error) bool {
	var he *httpError
	if errors.As(err, &he) {
		return he.Status == http.StatusNotFound || he.Status == http.StatusGone
	}
	return errors.Is(err, errTooLarge)
}

// parsePage reads one catalog page.
func parsePage(body []byte) ([]artifactRow, error) {
	var doc struct {
		Artifacts []struct {
			ID        int64     `json:"id"`
			CreatedAt time.Time `json:"created_at"`
			Expired   bool      `json:"expired"`
			Workflow  struct {
				HeadBranch string `json:"head_branch"`
			} `json:"workflow_run"`
		} `json:"artifacts"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, err
	}
	rows := make([]artifactRow, 0, len(doc.Artifacts))
	for _, a := range doc.Artifacts {
		rows = append(rows, artifactRow{ID: a.ID, CreatedAt: a.CreatedAt, Expired: a.Expired, Branch: a.Workflow.HeadBranch})
	}
	return rows, nil
}

// linkNext returns the rel="next" target of a Link header.
func linkNext(h string) string {
	for _, link := range strings.Split(h, ",") {
		parts := strings.Split(link, ";")
		if len(parts) < 2 {
			continue
		}
		for _, p := range parts[1:] {
			if strings.TrimSpace(p) == `rel="next"` {
				return strings.Trim(strings.TrimSpace(parts[0]), "<>")
			}
		}
	}
	return ""
}

// extract returns the record file from an artifact zip.
func extract(data []byte) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	for _, f := range zr.File {
		if path.Base(f.Name) != record.ArtifactFile {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		return readLimited(rc)
	}
	return nil, fmt.Errorf("no %s in the artifact", record.ArtifactFile)
}

// state is the cache's index: the last catalog refresh, the catalog pages with
// their validators, the head branch of each downloaded artifact's run, and the
// artifacts set aside as unusable. The record files say what is downloaded.
type state struct {
	Refreshed time.Time        `json:"refreshed"`
	Pages     []page           `json:"pages"`
	Branches  map[int64]string `json:"branches,omitempty"`
	Unusable  []int64          `json:"unusable,omitempty"`
}

type page struct {
	URL  string        `json:"url"`
	ETag string        `json:"etag,omitempty"`
	Rows []artifactRow `json:"rows"`
}

type artifactRow struct {
	ID        int64     `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	Expired   bool      `json:"expired,omitempty"`
	Branch    string    `json:"branch,omitempty"`
}

// loadState reads the cache index. A missing index starts an empty one; an
// unreadable one does too and says so. The cached record files stay readable
// either way.
func loadState(dir string) (state, error) {
	p := filepath.Join(dir, "state.json")
	data, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return state{}, nil
	}
	if err != nil {
		return state{}, err
	}
	var st state
	if err := json.Unmarshal(data, &st); err != nil {
		return state{}, fmt.Errorf("%s: %w", p, err)
	}
	return st, nil
}

func saveState(dir string, st state) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(filepath.Join(dir, "state.json"), append(data, '\n'))
}

// writeFile replaces path with data, leaving no partial file behind.
func writeFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
