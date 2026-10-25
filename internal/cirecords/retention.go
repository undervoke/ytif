// Retention for the CI record cache: age + byte budgets over the whole
// common-dir, enforced by evicting complete artifact files, oldest first.
// GitHub expiry never deletes cached data.
//
// Concurrency: Sync/GC serializes on a stable nonblocking lock
// (<ytif>/ci-cache.lock), distinct from local-record maintenance. Older
// writers ignore the lock, so read and pending recheck the persisted
// boundary and GC reclaims reappeared files on the next pass. Boundaries
// persist BEFORE any delete. Old state.json is read-only migration input;
// new state lives in state-v2.json, which old boards never write.
//
// Artifacts without recoverable creation metadata are never excluded or
// evicted. They stay counted and surface through overage.
package cirecords

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/undervoke/ytif/internal/storage"
)

const (
	controlFileName = "state-v2.json"
	ciCacheLockName = "ci-cache.lock"
)

// control is the private per-repository index.
type control struct {
	Refreshed time.Time           `json:"refreshed"`
	Pages     []page              `json:"pages"`
	Branches  map[int64]string    `json:"branches,omitempty"`
	Created   map[int64]time.Time `json:"created_at,omitempty"`
	Unusable  []int64             `json:"unusable,omitempty"`
	Boundary  *storage.Boundary   `json:"boundary,omitempty"`
}

func (c *control) ensureMaps() {
	if c.Branches == nil {
		c.Branches = map[int64]string{}
	}
	if c.Created == nil {
		c.Created = map[int64]time.Time{}
	}
}

// ciCacheRoot is the shared parent of every origin cache in one git
// common-dir. The byte budget spans all of them.
func ciCacheRoot(commonDir string) string {
	return filepath.Join(storage.Dir(commonDir), "ci-cache")
}

func ciCacheLockPath(commonDir string) string {
	return filepath.Join(storage.Dir(commonDir), ciCacheLockName)
}

func controlPath(dir string) string {
	return filepath.Join(dir, controlFileName)
}

// loadControl reads the private index, migrating read-only from the legacy
// state.json when v2 is absent. It never writes; callers persist through
// saveControl on their next mutation. A missing v2 and missing legacy is a
// fresh cache. A corrupt or unreadable v2, or a corrupt legacy during
// bootstrap, returns an error so callers defer destructive cleanup.
func loadControl(dir string) (control, state, error) {
	var ctl control
	data, err := storage.ReadFile(controlPath(dir))
	if err == nil {
		if uerr := json.Unmarshal(data, &ctl); uerr != nil {
			leg, _ := loadState(dir)
			return control{}, leg, fmt.Errorf("%s: %w", controlPath(dir), uerr)
		}
		ctl.ensureMaps()
		leg, legErr := loadState(dir)
		if legErr != nil {
			return ctl, state{}, nil
		}
		return ctl, leg, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return control{}, state{}, err
	}
	leg, legErr := loadState(dir)
	if legErr != nil {
		return control{}, state{}, legErr
	}
	ctl = control{
		Refreshed: leg.Refreshed,
		Pages:     leg.Pages,
		Branches:  leg.Branches,
		Unusable:  leg.Unusable,
	}
	ctl.ensureMaps()
	for _, p := range leg.Pages {
		for _, r := range p.Rows {
			if !r.CreatedAt.IsZero() {
				if _, ok := ctl.Created[r.ID]; !ok {
					ctl.Created[r.ID] = r.CreatedAt
				}
			}
			if r.Branch != "" {
				if _, ok := ctl.Branches[r.ID]; !ok {
					ctl.Branches[r.ID] = r.Branch
				}
			}
		}
	}
	return ctl, leg, nil
}

func saveControl(dir string, ctl control) error {
	return storage.WriteJSON(controlPath(dir), ctl)
}

// mergeLegacy fills IDs the control lacks from the legacy index: creation
// times, branches that outlive catalog expiry, and unusable marks.
func (s *syncer) mergeLegacy() {
	s.ctl.ensureMaps()
	for _, p := range s.st.Pages {
		for _, r := range p.Rows {
			if !r.CreatedAt.IsZero() {
				if _, ok := s.ctl.Created[r.ID]; !ok {
					s.ctl.Created[r.ID] = r.CreatedAt
				}
			}
			if r.Branch != "" {
				if _, ok := s.ctl.Branches[r.ID]; !ok {
					s.ctl.Branches[r.ID] = r.Branch
				}
			}
		}
	}
	for id, b := range s.st.Branches {
		if _, ok := s.ctl.Branches[id]; !ok && b != "" {
			s.ctl.Branches[id] = b
		}
	}
	if len(s.st.Unusable) > 0 {
		seen := map[int64]bool{}
		for _, id := range s.ctl.Unusable {
			seen[id] = true
		}
		for _, id := range s.st.Unusable {
			if !seen[id] {
				s.ctl.Unusable = append(s.ctl.Unusable, id)
				seen[id] = true
			}
		}
	}
}

// evicted reports whether a known creation observation is at or below the
// persisted boundary. Unknown observations are never evicted.
func evicted(created time.Time, id int64, b *storage.Boundary) bool {
	if b == nil || created.IsZero() {
		return false
	}
	return !b.Before(storage.Boundary{Time: created, ID: id})
}

func maxBoundary(a, b *storage.Boundary) *storage.Boundary {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	if a.Before(*b) {
		return b
	}
	return a
}

// combinedRows indexes catalog rows from the private control first, then the
// legacy index for IDs the control has never seen.
func (s *syncer) combinedRows() map[int64]artifactRow {
	out := map[int64]artifactRow{}
	for _, p := range s.ctl.Pages {
		for _, r := range p.Rows {
			if _, ok := out[r.ID]; !ok {
				out[r.ID] = r
			}
		}
	}
	for _, p := range s.st.Pages {
		for _, r := range p.Rows {
			if _, ok := out[r.ID]; !ok {
				out[r.ID] = r
			}
		}
	}
	return out
}

// createdFor resolves an artifact's creation time without any remote call:
// private control, then current/legacy catalog rows.
func (s *syncer) createdFor(id int64, rows map[int64]artifactRow) (time.Time, bool) {
	if t, ok := s.ctl.Created[id]; ok && !t.IsZero() {
		return t, true
	}
	if r, ok := rows[id]; ok && !r.CreatedAt.IsZero() {
		return r.CreatedAt, true
	}
	return time.Time{}, false
}

// scanCICache sums artifact record bytes across all origins in one common-dir.
func scanCICache(commonDir string) (managed int64, err error) {
	root := ciCacheRoot(commonDir)
	var problems []error
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if !errors.Is(walkErr, fs.ErrNotExist) {
				problems = append(problems, walkErr)
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		base := filepath.Base(p)
		if filepath.Base(filepath.Dir(p)) != "artifacts" {
			return nil
		}
		if len(base) <= len(".jsonl") || base[len(base)-len(".jsonl"):] != ".jsonl" {
			return nil
		}
		if _, err := strconv.ParseInt(base[:len(base)-len(".jsonl")], 10, 64); err != nil {
			return nil
		}
		if info, err := d.Info(); err == nil {
			managed += info.Size()
		} else if !errors.Is(err, fs.ErrNotExist) {
			problems = append(problems, err)
		}
		return nil
	})
	return managed, errors.Join(problems...)
}

func heldIn(dir string) []int64 {
	entries, _ := os.ReadDir(filepath.Join(dir, "artifacts"))
	var ids []int64
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
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

func artifactFile(dir string, id int64) string {
	return filepath.Join(dir, "artifacts", strconv.FormatInt(id, 10)+".jsonl")
}

// ciRepoDirs lists origin cache dirs holding at least one artifact file.
func ciRepoDirs(commonDir string) ([]string, error) {
	root := ciCacheRoot(commonDir)
	seen := map[string]bool{}
	var out []string
	var problems []error
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if !errors.Is(walkErr, fs.ErrNotExist) {
				problems = append(problems, walkErr)
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		base := filepath.Base(p)
		if len(base) <= len(".jsonl") || base[len(base)-len(".jsonl"):] != ".jsonl" {
			return nil
		}
		if _, err := strconv.ParseInt(base[:len(base)-len(".jsonl")], 10, 64); err != nil {
			return nil
		}
		if filepath.Base(filepath.Dir(p)) != "artifacts" {
			return nil
		}
		dir := filepath.Dir(filepath.Dir(p))
		if !seen[dir] {
			seen[dir] = true
			out = append(out, dir)
		}
		return nil
	})
	sort.Strings(out)
	return out, errors.Join(problems...)
}

// ciKey names a repo dir for the receipt, relative to the cache root.
func ciKey(commonDir, dir string) string {
	rel, err := filepath.Rel(ciCacheRoot(commonDir), dir)
	if err != nil || rel == "." || rel == ".." || len(rel) >= 3 && rel[:3] == ".."+string(filepath.Separator) {
		return filepath.ToSlash(dir)
	}
	return filepath.ToSlash(rel)
}

type gcItem struct {
	dir     string
	id      int64
	created time.Time
	size    int64
}

// runGC enforces age then bytes across all origin caches in the common-dir,
// evicting the global oldest first. Unknown-metadata artifacts are never
// evicted; they stay counted and surface through overage. Boundaries persist
// before that origin's unlinks. On invalid policy or an
// unreadable control, GC defers deletion but still reports totals and the
// reason.
func (s *syncer) runGC() {
	managed, scanErr := scanCICache(s.commonDir)
	budget := s.policy.CIMaxBytes
	overage := func(total int64) int64 {
		if total > budget {
			return total - budget
		}
		return 0
	}
	deferGC := func(reason string) {
		s.pool = storage.Pool{
			LastRun:      s.now,
			ManagedBytes: managed,
			Boundary:     s.ctl.Boundary,
			Error:        reason,
			Overage:      overage(managed),
		}
		if err := storage.UpdateReceipt(s.commonDir, "ci", ciKey(s.commonDir, s.dir), s.policy, s.pool); err != nil {
			s.problems = append(s.problems, fmt.Errorf("ci receipt: %w", err))
		}
	}
	if !s.policyValid {
		deferGC("storage policy invalid; destructive cleanup deferred")
		return
	}
	if !s.ctlValid {
		deferGC("ci control unreadable; destructive cleanup deferred")
		return
	}
	if scanErr != nil {
		s.problems = append(s.problems, scanErr)
		deferGC(scanErr.Error())
		return
	}

	// Inventory every origin cache. Other repos load read-only here; their
	// controls are only rewritten if they lose artifacts below.
	type repoInv struct {
		ctl  control
		rows map[int64]artifactRow
	}
	invs := map[string]*repoInv{}
	invs[s.dir] = &repoInv{ctl: s.ctl, rows: s.combinedRows()}
	dirs, err := ciRepoDirs(s.commonDir)
	if err != nil {
		s.problems = append(s.problems, err)
		deferGC(err.Error())
		return
	}
	for _, dir := range dirs {
		if _, ok := invs[dir]; ok {
			continue
		}
		ctl, leg, err := loadControl(dir)
		if err != nil {
			s.problems = append(s.problems, fmt.Errorf("%s: %w; retained without eviction", dir, err))
			continue
		}
		rows := map[int64]artifactRow{}
		for _, p := range ctl.Pages {
			for _, r := range p.Rows {
				if _, ok := rows[r.ID]; !ok {
					rows[r.ID] = r
				}
			}
		}
		for _, p := range leg.Pages {
			for _, r := range p.Rows {
				if _, ok := rows[r.ID]; !ok {
					rows[r.ID] = r
				}
			}
		}
		invs[dir] = &repoInv{ctl: ctl, rows: rows}
	}

	createdFor := func(inv *repoInv, id int64) (time.Time, bool) {
		if t, ok := inv.ctl.Created[id]; ok && !t.IsZero() {
			return t, true
		}
		if r, ok := inv.rows[id]; ok && !r.CreatedAt.IsZero() {
			return r.CreatedAt, true
		}
		return time.Time{}, false
	}
	var known []gcItem
	for dir, inv := range invs {
		for _, id := range heldIn(dir) {
			info, err := os.Stat(artifactFile(dir, id))
			if err != nil {
				continue
			}
			if t, ok := createdFor(inv, id); ok {
				known = append(known, gcItem{dir: dir, id: id, created: t, size: info.Size()})
			}
		}
	}
	sort.Slice(known, func(i, j int) bool {
		if !known[i].created.Equal(known[j].created) {
			return known[i].created.Before(known[j].created)
		}
		if known[i].id != known[j].id {
			return known[i].id < known[j].id
		}
		return known[i].dir < known[j].dir
	})

	cutoff := s.now.AddDate(0, 0, -s.policy.Days)
	var evict []gcItem
	evictSet := map[gcItem]bool{}
	for _, c := range known {
		if c.created.Before(cutoff) || evicted(c.created, c.id, invs[c.dir].ctl.Boundary) {
			evict = append(evict, c)
			evictSet[c] = true
		}
	}
	remaining := make([]gcItem, 0, len(known))
	keptTotal := managed
	for _, c := range evict {
		keptTotal -= c.size
	}
	for _, c := range known {
		if !evictSet[c] {
			remaining = append(remaining, c)
		}
	}
	for len(remaining) > 0 && keptTotal > budget {
		c := remaining[0]
		remaining = remaining[1:]
		evict = append(evict, c)
		keptTotal -= c.size
	}

	if len(evict) == 0 {
		s.pool = storage.Pool{
			LastRun:      s.now,
			ManagedBytes: managed,
			Boundary:     s.ctl.Boundary,
			Overage:      overage(managed),
		}
		if err := storage.UpdateReceipt(s.commonDir, "ci", ciKey(s.commonDir, s.dir), s.policy, s.pool); err != nil {
			s.problems = append(s.problems, fmt.Errorf("ci receipt: %w", err))
		}
		return
	}

	// Finish each origin after persisting its boundary, so a later save
	// failure preserves the cleanup already completed.
	byRepo := map[string][]gcItem{}
	var repoOrder []string
	for _, c := range evict {
		if _, ok := byRepo[c.dir]; !ok {
			repoOrder = append(repoOrder, c.dir)
		}
		byRepo[c.dir] = append(byRepo[c.dir], c)
	}
	reclaimedByRepo := map[string]*struct {
		bytes int64
		units int
	}{}
	for _, dir := range repoOrder {
		items := byRepo[dir]
		inv := invs[dir]
		next := inv.ctl
		next.Created = maps.Clone(inv.ctl.Created)
		next.ensureMaps()
		for _, c := range items {
			next.Boundary = maxBoundary(next.Boundary, &storage.Boundary{Time: c.created, ID: c.id})
		}
		// Creation metadata identifies an evicted file if an older board
		// restores it after its catalog row has expired.
		for _, c := range items {
			next.Created[c.id] = c.created
		}
		if err := saveControl(dir, next); err != nil {
			s.problems = append(s.problems, fmt.Errorf("%s: %w; remaining cleanup deferred", dir, err))
			break
		}
		inv.ctl = next
		if dir == s.dir {
			s.ctl = next
		}
		for _, c := range items {
			if err := os.Remove(artifactFile(dir, c.id)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				s.problems = append(s.problems, fmt.Errorf("artifact %d: %w", c.id, err))
				continue
			}
			r := reclaimedByRepo[dir]
			if r == nil {
				r = &struct {
					bytes int64
					units int
				}{}
				reclaimedByRepo[dir] = r
			}
			r.bytes += c.size
			r.units++
		}
	}
	postManaged, scanErr := scanCICache(s.commonDir)
	if scanErr != nil {
		s.problems = append(s.problems, scanErr)
	}
	for dir := range byRepo {
		inv := invs[dir]
		r := reclaimedByRepo[dir]
		var rb int64
		var ru int
		if r != nil {
			rb, ru = r.bytes, r.units
		}
		pool := storage.Pool{
			LastRun:        s.now,
			ReclaimedBytes: rb,
			ReclaimedUnits: ru,
			ManagedBytes:   postManaged,
			Boundary:       inv.ctl.Boundary,
			Overage:        overage(postManaged),
		}
		if err := storage.UpdateReceipt(s.commonDir, "ci", ciKey(s.commonDir, dir), s.policy, pool); err != nil {
			s.problems = append(s.problems, fmt.Errorf("ci receipt: %w", err))
		}
		if dir == s.dir {
			s.pool = pool
		}
	}
	if _, ok := byRepo[s.dir]; !ok {
		s.pool = storage.Pool{
			LastRun:      s.now,
			ManagedBytes: postManaged,
			Boundary:     s.ctl.Boundary,
			Overage:      overage(postManaged),
		}
		if err := storage.UpdateReceipt(s.commonDir, "ci", ciKey(s.commonDir, s.dir), s.policy, s.pool); err != nil {
			s.problems = append(s.problems, fmt.Errorf("ci receipt: %w", err))
		}
	}
}

// cachedSnapshot serves a locked-out or lock-failed Sync from committed state:
// no catalog refresh, no downloads, no GC, no receipt write, no control save.
func cachedSnapshot(commonDir string, repo Repo, dir string, now time.Time, cause error) (Result, error) {
	// Load returns defaults with the error, so the snapshot still reports a
	// sensible budget while GC stays deferred.
	policy, policyErr := storage.Load(commonDir)
	ctl, legacy, ctlErr := loadControl(dir)
	s := &syncer{
		repo: repo, dir: dir, commonDir: commonDir,
		st: legacy, ctl: ctl, ctlValid: ctlErr == nil,
		policy: policy, policyValid: policyErr == nil,
		now: now,
	}
	s.mergeLegacy()
	lines, branch := s.read()
	managed, scanErr := scanCICache(commonDir)
	errText := cause.Error()
	if ctlErr != nil {
		errText += "; ci control: " + ctlErr.Error()
	}
	s.pool = storage.Pool{
		LastRun:      now,
		ManagedBytes: managed,
		Boundary:     s.ctl.Boundary,
		Error:        errText,
	}
	st := s.status()
	st.Kind = StatusOffline
	errs := []error{cause}
	if scanErr != nil {
		errs = append(errs, scanErr)
	}
	if ctlErr != nil {
		errs = append(errs, ctlErr)
	}
	errs = append(errs, s.problems...)
	return Result{Lines: lines, Branch: branch, Status: st}, errors.Join(errs...)
}
