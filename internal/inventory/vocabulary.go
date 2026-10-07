package inventory

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Languages lists the languages labels may be declared in.
var Languages = []string{"en", "ko"}

// Label is a display name: one text for every language, or one per language.
type Label struct {
	Text   string            // set when one text serves every language
	ByLang map[string]string // otherwise, language → text
}

// UnmarshalYAML accepts a string or a map from language to string.
func (l *Label) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		if strings.TrimSpace(n.Value) == "" {
			return fmt.Errorf("line %d: empty label", n.Line)
		}
		*l = Label{Text: n.Value}
		return nil
	case yaml.MappingNode:
		by := map[string]string{}
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			if !isLanguage(k.Value) {
				return fmt.Errorf("line %d: label language %q: want one of %s", k.Line, k.Value, strings.Join(Languages, ", "))
			}
			if v.Kind != yaml.ScalarNode || strings.TrimSpace(v.Value) == "" {
				return fmt.Errorf("line %d: %s label must be non-empty text", v.Line, k.Value)
			}
			by[k.Value] = v.Value
		}
		if len(by) == 0 {
			return fmt.Errorf("line %d: empty label", n.Line)
		}
		*l = Label{ByLang: by}
		return nil
	}
	return fmt.Errorf("line %d: a label is text or a map from language to text", n.Line)
}

// MarshalYAML writes the label back in its declared form.
func (l Label) MarshalYAML() (any, error) {
	if l.ByLang != nil {
		return l.ByLang, nil
	}
	return l.Text, nil
}

func isLanguage(s string) bool {
	for _, l := range Languages {
		if s == l {
			return true
		}
	}
	return false
}

// Vocabulary holds the project's own tag groups in declaration order.
type Vocabulary []Group

// Group is one tag group. Built-in groups carry their own labels; a project
// group is named by its key.
type Group struct {
	Name       string
	Heading    string // built-in only: the question the group answers
	Label      Label  // built-in only
	ExactlyOne bool   // every check carries exactly one of its tags
	Tags       []Tag
}

// Tag is one vocabulary term.
type Tag struct {
	Name  string
	Label Label
}

// UnmarshalYAML reads group → tag → label, keeping declaration order.
func (v *Vocabulary) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: vocabulary maps a group to its tags", n.Line)
	}
	var out Vocabulary
	for i := 0; i+1 < len(n.Content); i += 2 {
		gk, gv := n.Content[i], n.Content[i+1]
		if gv.Kind != yaml.MappingNode {
			return fmt.Errorf("line %d: group %q maps a tag to its label", gv.Line, gk.Value)
		}
		g := Group{Name: gk.Value}
		for j := 0; j+1 < len(gv.Content); j += 2 {
			tk, tv := gv.Content[j], gv.Content[j+1]
			var l Label
			if err := tv.Decode(&l); err != nil {
				return fmt.Errorf("tag %q: %w", tk.Value, err)
			}
			g.Tags = append(g.Tags, Tag{Name: tk.Value, Label: l})
		}
		out = append(out, g)
	}
	*v = out
	return nil
}

// MarshalYAML writes the vocabulary as group → tag → label.
func (v Vocabulary) MarshalYAML() (any, error) {
	root := &yaml.Node{Kind: yaml.MappingNode}
	for _, g := range v {
		tags := &yaml.Node{Kind: yaml.MappingNode}
		for _, t := range g.Tags {
			var label yaml.Node
			if err := label.Encode(t.Label); err != nil {
				return nil, err
			}
			tags.Content = append(tags.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: t.Name}, &label)
		}
		root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: g.Name}, tags)
	}
	return root, nil
}

// validate rejects project groups that reuse a built-in group name and tag
// names declared twice, since a tag alone must identify its group.
func (v Vocabulary) validate() error {
	var errs []error
	seenGroup := map[string]bool{}
	for _, g := range Builtin {
		seenGroup[g.Name] = true
	}
	owner := map[string]string{}
	for _, g := range Builtin {
		for _, t := range g.Tags {
			owner[t.Name] = g.Name
		}
	}
	for _, g := range v {
		switch {
		case strings.TrimSpace(g.Name) == "":
			errs = append(errs, errors.New("a group name is empty"))
		case seenGroup[g.Name]:
			errs = append(errs, fmt.Errorf("group %q is built in or declared twice; declare project tags in a group of their own", g.Name))
		}
		seenGroup[g.Name] = true
		for _, t := range g.Tags {
			switch prev, ok := owner[t.Name]; {
			case strings.TrimSpace(t.Name) == "":
				errs = append(errs, fmt.Errorf("group %q: a tag name is empty", g.Name))
			case ok:
				errs = append(errs, fmt.Errorf("tag %q in group %q is already in group %q", t.Name, g.Name, prev))
			}
			owner[t.Name] = g.Name
		}
	}
	return errors.Join(errs...)
}

// Groups returns the built-in groups followed by the project's.
func (inv Inventory) Groups() []Group {
	return append(append([]Group(nil), Builtin...), inv.Vocabulary...)
}

// TagGroups maps every known tag to its group.
func (inv Inventory) TagGroups() map[string]Group {
	m := map[string]Group{}
	for _, g := range inv.Groups() {
		for _, t := range g.Tags {
			m[t.Name] = g
		}
	}
	return m
}

// TagProblems reports the entry's unknown tags and the exactly-one groups it
// does not tag exactly once.
func (e Entry) TagProblems(groups map[string]Group) (unknown []string, counts []string) {
	n := map[string]int{}
	for _, t := range e.Tags {
		g, ok := groups[t]
		if !ok {
			unknown = append(unknown, t)
			continue
		}
		n[g.Name]++
	}
	for _, g := range Builtin {
		if g.ExactlyOne && n[g.Name] != 1 {
			counts = append(counts, fmt.Sprintf("%s has %d tags, want exactly 1", g.Name, n[g.Name]))
		}
	}
	sort.Strings(unknown)
	return unknown, counts
}

func both(en, ko string) Label { return Label{ByLang: map[string]string{"en": en, "ko": ko}} }

// Builtin is the vocabulary every project shares.
var Builtin = []Group{
	{Name: "what", Heading: "What", Label: both("Harm", "피해"), Tags: []Tag{
		{"code-execution", both("Tampered code runs", "변조 코드 실행")},
		{"permission-bypass", both("Permission bypass", "권한 우회")},
		{"exposure", both("Secret or data exposure", "비밀·데이터 노출")},
		{"data-loss", both("Data loss", "데이터 손실")},
		{"misdirection", both("Wrong target", "잘못된 대상")},
		{"outage", both("Outage", "기능 마비")},
	}},
	{Name: "who", Heading: "Who", Label: both("From whom", "누구로부터"), Tags: []Tag{
		{"user", both("User", "사용자")},
		{"agent", both("Agent", "agent")},
		{"attacker", both("Attacker", "공격자")},
		{"fault", both("Fault", "장애")},
	}},
	{Name: "why", Heading: "Why", Label: both("Behavior prevented", "막으려는 행위"), Tags: []Tag{
		{"habit", both("Habit", "무의식적 행동")},
		{"mistake", both("Mistake", "실수")},
		{"attack", both("Attack", "악의적 공격")},
	}},
	{Name: "visibility", Heading: "How", Label: both("Surfacing", "드러남"), ExactlyOne: true, Tags: []Tag{
		{"silent", both("Silent", "조용한")},
		{"visible", both("Visible", "드러나는")},
	}},
	{Name: "recovery", Heading: "How", Label: both("Recovery", "복구"), ExactlyOne: true, Tags: []Tag{
		{"irreversible", both("Irreversible", "불가역적")},
		{"manual", both("Manual recovery", "수동 복구")},
	}},
}
