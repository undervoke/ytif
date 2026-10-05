package record

import "sort"

// Filter restricts which lines a query reads. Empty fields match everything.
type Filter struct {
	Gate    string
	Context string
}

func (f Filter) match(l Line) bool {
	return (f.Gate == "" || l.Gate == f.Gate) && (f.Context == "" || l.Context == f.Context)
}

// Group is what statistics are computed per: costs differ by gate and
// context, so they are never averaged together.
type Group struct {
	Gate    string
	Context string
}

// CheckStat summarizes one check in one gate and context. Fails counts every
// fail outcome; each one is a hit. AvgMS averages the timed outcomes among
// the last N, and is nil when none of them carries elapsed time.
type CheckStat struct {
	Group
	Key   string
	Runs  int
	Fails int
	Timed int // timed outcomes inside the window
	AvgMS *float64
}

// InvocationStat summarizes the wall time of one runner's builds or
// processes for one unit in one gate and context; the unit is where a
// shared build or process cost is decided.
type InvocationStat struct {
	Group
	Runner string
	What   string
	Unit   string
	Runs   int
	Timed  int
	AvgMS  *float64
}

// GuardStat counts the agent commands the guard refused per runner.
type GuardStat struct {
	Group
	Runner   string
	Refusals int
}

// Stats computes check, invocation, and guard statistics; averages cover
// the last n observations of each subject, ordered by recorded time.
func Stats(lines []Line, f Filter, n int) ([]CheckStat, []InvocationStat, []GuardStat) {
	ordered := append([]Line(nil), lines...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Time.Before(ordered[j].Time) })

	type checkID struct {
		Group
		key string
	}
	type invID struct {
		Group
		runner, what, unit string
	}
	type guardID struct {
		Group
		runner string
	}
	checks := map[checkID][]Line{}
	invs := map[invID][]Line{}
	guards := map[guardID]int{}
	for _, l := range ordered {
		if !f.match(l) {
			continue
		}
		g := Group{l.Gate, l.Context}
		switch l.Kind {
		case KindResult:
			id := checkID{g, l.Key}
			checks[id] = append(checks[id], l)
		case KindInvocation:
			id := invID{g, l.Runner, l.What, l.Unit}
			invs[id] = append(invs[id], l)
		case KindGuard:
			guards[guardID{g, l.Runner}]++
		}
	}

	var cs []CheckStat
	for id, ls := range checks {
		s := CheckStat{Group: id.Group, Key: id.key, Runs: len(ls)}
		for _, l := range ls {
			if l.Outcome == "fail" {
				s.Fails++
			}
		}
		s.Timed, s.AvgMS = average(window(ls, n))
		cs = append(cs, s)
	}
	sort.Slice(cs, func(i, j int) bool {
		if cs[i].Key != cs[j].Key {
			return cs[i].Key < cs[j].Key
		}
		return groupLess(cs[i].Group, cs[j].Group)
	})

	var is []InvocationStat
	for id, ls := range invs {
		s := InvocationStat{Group: id.Group, Runner: id.runner, What: id.what, Unit: id.unit, Runs: len(ls)}
		s.Timed, s.AvgMS = average(window(ls, n))
		is = append(is, s)
	}
	sort.Slice(is, func(i, j int) bool {
		if is[i].Runner != is[j].Runner {
			return is[i].Runner < is[j].Runner
		}
		if is[i].What != is[j].What {
			return is[i].What < is[j].What
		}
		if is[i].Unit != is[j].Unit {
			return is[i].Unit < is[j].Unit
		}
		return groupLess(is[i].Group, is[j].Group)
	})

	var gs []GuardStat
	for id, count := range guards {
		gs = append(gs, GuardStat{Group: id.Group, Runner: id.runner, Refusals: count})
	}
	sort.Slice(gs, func(i, j int) bool {
		if gs[i].Runner != gs[j].Runner {
			return gs[i].Runner < gs[j].Runner
		}
		return groupLess(gs[i].Group, gs[j].Group)
	})
	return cs, is, gs
}

func groupLess(a, b Group) bool {
	if a.Gate != b.Gate {
		return a.Gate < b.Gate
	}
	return a.Context < b.Context
}

func window(ls []Line, n int) []Line {
	if n > 0 && len(ls) > n {
		return ls[len(ls)-n:]
	}
	return ls
}

func average(ls []Line) (int, *float64) {
	var sum int64
	timed := 0
	for _, l := range ls {
		if l.ElapsedMS != nil {
			sum += *l.ElapsedMS
			timed++
		}
	}
	if timed == 0 {
		return 0, nil
	}
	avg := float64(sum) / float64(timed)
	return timed, &avg
}
