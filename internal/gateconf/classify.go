package gateconf

import (
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/undervoke/ytif/internal/shellcmd"
)

// class is what one simple command executes.
type class int

const (
	neutral  class = iota // shell built-ins that run no code
	rail                  // ytif itself
	external              // code outside the repository
	repoCode              // repository code, which must run through ytif
	unknown               // a command whose execution cannot be resolved statically
)

// neutralCommands run no code of their own; the scripts trap and eval run
// are listed as commands of their own.
var neutralCommands = map[string]bool{
	"cd": true, "pushd": true, "popd": true, "echo": true, "printf": true, "export": true, "set": true, "unset": true,
	"true": true, "false": true, "exit": true, "test": true, "[": true, ":": true, "pwd": true, "read": true,
	"shift": true, "return": true, "local": true, "readonly": true, "declare": true, "trap": true, "eval": true, "wait": true,
}

// scope is the working directory an entry's commands run in.
type scope struct {
	dir   string // relative to the repository root
	known bool   // false when the directory cannot be resolved, or the script changes it
}

var rootScope = scope{dir: ".", known: true}

// classifier resolves commands against the repository.
type classifier struct {
	root    string
	modules []string        // module paths of tracked go.mod files
	names   map[string]bool // every path element of a tracked file
	tracked []string
}

func newClassifier(root string, tracked []string) *classifier {
	c := &classifier{root: root, names: map[string]bool{}, tracked: tracked}
	for _, f := range tracked {
		for _, el := range strings.Split(f, "/") {
			c.names[el] = true
		}
		if path.Base(f) != "go.mod" {
			continue
		}
		if mod := modulePath(filepath.Join(root, filepath.FromSlash(f))); mod != "" {
			c.modules = append(c.modules, mod)
		}
	}
	return c
}

// modulePath reads the module directive of a go.mod file.
func modulePath(file string) string {
	data, err := os.ReadFile(file)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "module"); ok && (rest == "" || rest[0] == ' ' || rest[0] == '\t') {
			mod := strings.TrimSpace(strings.SplitN(rest, "//", 2)[0])
			return strings.Trim(mod, "\"`")
		}
	}
	return ""
}

// script classifies every command of a shell script and returns the most
// severe class.
func (c *classifier) script(text string, sc scope) (class, string) {
	cmds, err := shellcmd.Parse(text)
	if err != nil {
		return unknown, "cannot parse the script: " + err.Error()
	}
	return c.worst(cmds, sc)
}

// command classifies the command a wrapper runs, with the commands that
// command runs itself, and returns the most severe class.
func (c *classifier) command(cmd shellcmd.Command, sc scope) (class, string) {
	cmds, err := shellcmd.Expand(shellcmd.Unwrap(cmd))
	if err != nil {
		return unknown, "cannot parse the script: " + err.Error()
	}
	return c.worst(cmds, sc)
}

func (c *classifier) worst(cmds []shellcmd.Command, sc scope) (class, string) {
	sc = within(cmds, sc)
	worst, why := neutral, ""
	for _, cmd := range cmds {
		cls, w := c.classify(cmd, sc)
		if severity[cls] > severity[worst] {
			worst, why = cls, w
		}
	}
	return worst, why
}

// within returns sc, unless cmds change directory, after which relative
// paths no longer resolve against it.
func within(cmds []shellcmd.Command, sc scope) scope {
	for _, cmd := range cmds {
		switch cmd.Name() {
		case "cd", "pushd", "popd":
			sc.known = false
		}
	}
	return sc
}

var severity = map[class]int{neutral: 0, rail: 0, external: 1, unknown: 2, repoCode: 3}

// classify returns what cmd executes and, unless external, why.
func (c *classifier) classify(cmd shellcmd.Command, sc scope) (class, string) {
	if cmd.Unresolved != "" {
		return unknown, cmd.Unresolved
	}
	if len(cmd.Args) == 0 {
		return neutral, ""
	}
	if cmd.NameBase() == "ytif" {
		return rail, ""
	}
	if r := TestRunner(cmd); r != "" {
		return repoCode, "runs " + r + " outside ytif"
	}
	name := cmd.Name()
	if name == "" {
		return unknown, "the command name is not literal"
	}
	if strings.Contains(name, "/") {
		switch c.place(name, sc) {
		case inRepo:
			return repoCode, "runs a repository file"
		case installed:
			return external, ""
		}
		name = path.Base(name) // an installed program, known by its name
	}
	if neutralCommands[name] {
		return neutral, ""
	}
	switch name {
	case "source", ".":
		return c.sourced(cmd, sc)
	case "sh", "bash", "zsh", "dash", "ksh":
		return c.shell(cmd, sc)
	case "go":
		return c.goCommand(cmd)
	case "node":
		return c.node(cmd, sc)
	case "bun":
		return c.bun(cmd, sc)
	case "deno":
		return c.deno(cmd, sc)
	case "npm":
		return c.npm(cmd, sc)
	case "npx", "pnpx", "bunx":
		return c.execWrapper(cmd, 1, sc)
	case "pnpm":
		return c.pnpm(cmd, sc)
	case "yarn":
		return c.yarn(cmd, sc)
	case "dotnet":
		return c.dotnet(cmd, sc)
	case "make", "gmake", "just", "task", "rake", "mage", "turbo", "nx", "lerna", "npm-run-all", "run-s", "run-p":
		return repoCode, "runs repository-defined tasks"
	case "ruby", "perl", "php", "tsx", "ts-node", "pwsh", "powershell":
		return c.interpreter(cmd, 1, sc)
	}
	if name == "python" || strings.HasPrefix(name, "python3") || strings.HasPrefix(name, "python2") {
		return c.interpreter(cmd, 1, sc)
	}
	return external, ""
}

// place is where a word points, if it is a path.
type place int

const (
	notPath   place = iota
	inRepo          // repository content
	installed       // an installed dependency under node_modules
	outside         // outside the repository
)

// scheme matches specifiers such as node:fs or npm:pkg, which are not paths.
var scheme = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*:[^/\\]`)

// place resolves a word against the repository. A word with a slash is a
// path; a bare word is one only when it names something that exists. A
// relative path whose base cannot be resolved counts as repository content,
// since gate entries run inside the checkout.
func (c *classifier) place(p string, sc scope) place {
	if p == "" || p == "-" || strings.Contains(p, "://") || scheme.MatchString(p) {
		return notPath
	}
	if strings.HasPrefix(p, "~") {
		return outside
	}
	if filepath.IsAbs(p) {
		r, err := filepath.Rel(c.root, p)
		if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
			return outside
		}
		return repoPlace(filepath.ToSlash(r))
	}
	if !strings.Contains(p, "/") && !c.exists(p, sc) {
		return notPath
	}
	if !sc.known {
		return repoPlace(p)
	}
	rel := path.Clean(path.Join(sc.dir, p))
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return outside
	}
	return repoPlace(rel)
}

func repoPlace(rel string) place {
	for _, el := range strings.Split(rel, "/") {
		if el == "node_modules" {
			return installed
		}
	}
	return inRepo
}

// exists reports whether a bare name exists in the working directory, or,
// when that is unknown, anywhere among tracked files.
func (c *classifier) exists(name string, sc scope) bool {
	if !sc.known {
		return c.names[name]
	}
	_, err := os.Stat(filepath.Join(c.root, filepath.FromSlash(sc.dir), name))
	return err == nil
}

// sourced classifies source FILE and . FILE, which run FILE in this shell.
func (c *classifier) sourced(cmd shellcmd.Command, sc scope) (class, string) {
	if len(cmd.Args) < 2 {
		return neutral, ""
	}
	if !cmd.Literal[1] {
		return unknown, "the sourced file is not literal"
	}
	if c.place(cmd.Args[1], sc) == inRepo {
		return repoCode, "runs a repository script"
	}
	return external, ""
}

// shell classifies sh and its relatives by their script file or input; the
// script of -c or a here-document is listed as commands of its own.
func (c *classifier) shell(cmd shellcmd.Command, sc scope) (class, string) {
	for i := 1; i < len(cmd.Args); i++ {
		a := cmd.Args[i]
		switch {
		case a == "-o" || a == "+o":
			i++
		case a == "--version" || a == "--help":
			return neutral, ""
		case a == "--" || (!strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "+")):
			if a == "--" {
				if i++; i >= len(cmd.Args) {
					return neutral, ""
				}
			}
			if c.place(cmd.Args[i], sc) == inRepo {
				return repoCode, "runs a repository script"
			}
			return external, ""
		case !strings.HasPrefix(a, "--") && strings.ContainsRune(a[1:], 'c'):
			return neutral, ""
		}
	}
	if f := cmd.Stdin.File; f != "" {
		if c.place(f, sc) == inRepo {
			return repoCode, "runs a repository script from standard input"
		}
		return external, ""
	}
	return neutral, ""
}

// TestRunner returns the test runner cmd starts — "go test", "bun test",
// "node --test", or "dotnet test" — directly or through a package manager's
// exec, or "" for none. A command name known only by its last path element,
// such as "$GOROOT/bin/go", counts by that element. The agent guard shares
// this rule.
func TestRunner(cmd shellcmd.Command) string {
	name := cmd.NameBase()
	switch name {
	case "go":
		if i, ok := goSubcommand(cmd); ok && i < len(cmd.Args) && cmd.Args[i] == "test" {
			return "go test"
		}
	case "bun":
		if i, ok := bunSubcommand(cmd); ok && i < len(cmd.Args) {
			switch cmd.Args[i] {
			case "test":
				return "bun test"
			case "x":
				return execRunner(cmd, i+1)
			}
		}
	case "node":
		if nodeTestFlag(cmd) {
			return "node --test"
		}
	case "dotnet":
		if i, ok := operand(cmd, 1, nil); ok && i < len(cmd.Args) && cmd.Args[i] == "test" {
			return "dotnet test"
		}
	case "npx", "pnpx", "bunx":
		return execRunner(cmd, 1)
	case "npm", "pnpm", "yarn":
		values := map[string]map[string]bool{"npm": npmValueFlags, "pnpm": pnpmValueFlags, "yarn": yarnValueFlags}[name]
		if i, ok := operand(cmd, 1, values); ok && i < len(cmd.Args) {
			switch sub := cmd.Args[i]; {
			case sub == "exec", sub == "x" && name == "npm", sub == "dlx" && name != "npm":
				return execRunner(cmd, i+1)
			case sub == "node" && name == "yarn" && nodeTestFlag(cmd.Rest(i)):
				return "node --test"
			}
		}
	}
	return ""
}

// execRunner returns the test runner a package manager's exec starts: the
// command from word i, or the script of -c.
func execRunner(cmd shellcmd.Command, i int) string {
	for ; i < len(cmd.Args); i++ {
		if !cmd.Literal[i] {
			return ""
		}
		a := cmd.Args[i]
		switch {
		case a == "-c" || a == "--call":
			if i+1 < len(cmd.Args) && cmd.Literal[i+1] {
				return scriptRunner(cmd.Args[i+1])
			}
			return ""
		case strings.HasPrefix(a, "--call="):
			return scriptRunner(strings.TrimPrefix(a, "--call="))
		case a == "--":
			if i+1 < len(cmd.Args) {
				return commandRunner(cmd.Rest(i + 1))
			}
			return ""
		case strings.HasPrefix(a, "-"):
			if execValueFlags[a] {
				i++
			}
		default:
			return commandRunner(cmd.Rest(i))
		}
	}
	return ""
}

// commandRunner returns the test runner a wrapped command starts, itself or
// through the commands it runs.
func commandRunner(cmd shellcmd.Command) string {
	cmds, _ := shellcmd.Expand(shellcmd.Unwrap(cmd))
	for _, c := range cmds {
		if r := TestRunner(c); r != "" {
			return r
		}
	}
	return ""
}

func scriptRunner(script string) string {
	cmds, _ := shellcmd.Parse(script)
	for _, c := range cmds {
		if r := TestRunner(c); r != "" {
			return r
		}
	}
	return ""
}

// goSubcommand returns the index of go's subcommand after its -C option;
// ok is false when the subcommand is not literal.
func goSubcommand(cmd shellcmd.Command) (int, bool) {
	i := 1
	for i < len(cmd.Args) && cmd.Literal[i] && strings.HasPrefix(cmd.Args[i], "-") {
		if cmd.Args[i] == "-C" {
			i++ // -C dir; -C=dir is one word
		}
		i++
	}
	return i, i >= len(cmd.Args) || cmd.Literal[i]
}

// bunSubcommand returns the index of bun's subcommand or file, or the end
// when bun runs inline code instead.
func bunSubcommand(cmd shellcmd.Command) (int, bool) {
	for i := 1; i < len(cmd.Args); i++ {
		if !cmd.Literal[i] {
			return i, false
		}
		switch a := cmd.Args[i]; {
		case a == "--":
			return i + 1, i+1 >= len(cmd.Args) || cmd.Literal[i+1]
		case inlineFlags["node"][a]:
			return len(cmd.Args), true
		case !strings.HasPrefix(a, "-") || a == "-":
			return i, true
		case bunValueFlags[a]:
			i++
		}
	}
	return len(cmd.Args), true
}

var nodeValueFlags = set("-r", "--require", "--import", "--loader", "--experimental-loader", "-C", "--conditions",
	"--input-type", "--inspect-port", "--title", "--test-reporter", "--test-reporter-destination",
	"--test-name-pattern", "--test-skip-pattern", "--test-concurrency", "--test-timeout", "--test-shard",
	"--watch-path", "--env-file", "--env-file-if-exists", "--disable-warning", "-e", "--eval", "-p", "--print")

// nodeTestFlag reports whether --test is among node's own options, which
// end at the script or at --.
func nodeTestFlag(cmd shellcmd.Command) bool {
	for i := 1; i < len(cmd.Args); i++ {
		if !cmd.Literal[i] {
			return false
		}
		switch a := cmd.Args[i]; {
		case a == "--test":
			return true
		case a == "--" || a == "-" || !strings.HasPrefix(a, "-"):
			return false
		case nodeValueFlags[a]:
			i++
		}
	}
	return false
}

// goCommand classifies go subcommands; go run of the rail and go tool ytif
// are the rail.
func (c *classifier) goCommand(cmd shellcmd.Command) (class, string) {
	i, ok := goSubcommand(cmd)
	if i >= len(cmd.Args) {
		return external, ""
	}
	if !ok {
		return unknown, "the go subcommand is not literal"
	}
	switch cmd.Args[i] {
	case "env", "version":
		return neutral, "" // reads configuration, e.g. to locate ytif
	case "generate":
		return repoCode, "runs repository go:generate directives"
	case "tool":
		if j, ok := goToolName(cmd.Args[i+1:], cmd.Literal[i+1:]); ok && isYtifTool(cmd.Args[i+1+j]) {
			return rail, ""
		}
		return external, ""
	case "run":
	default:
		return external, ""
	}
	j, ok := goRunPackage(cmd.Args[i+1:], cmd.Literal[i+1:])
	if !ok {
		return unknown, "the go run package is not literal"
	}
	bare, _, versioned := strings.Cut(cmd.Args[i+1+j], "@")
	switch {
	case strings.HasSuffix(bare, "/cmd/ytif") || bare == "cmd/ytif":
		return rail, ""
	case bare == "." || strings.HasPrefix(bare, "./") || strings.HasPrefix(bare, "../") || strings.HasSuffix(bare, ".go"):
		return repoCode, "runs a repository package"
	case versioned:
		return external, ""
	}
	for _, mod := range c.modules {
		if bare == mod || strings.HasPrefix(bare, mod+"/") {
			return repoCode, "runs a repository package"
		}
	}
	return external, ""
}

// goBuildValueFlags take a separate value word.
var goBuildValueFlags = map[string]bool{
	"-C": true, "-tags": true, "-ldflags": true, "-gcflags": true, "-asmflags": true, "-mod": true, "-modfile": true,
	"-overlay": true, "-exec": true, "-p": true, "-pgo": true, "-toolexec": true, "-buildmode": true, "-compiler": true,
	"-installsuffix": true, "-pkgdir": true, "-o": true, "-covermode": true, "-coverpkg": true, "-gccgoflags": true,
}

// goRunPackage returns the index of the package go run builds.
func goRunPackage(args []string, lits []bool) (int, bool) {
	for i := 0; i < len(args); i++ {
		if !lits[i] {
			return 0, false
		}
		a := args[i]
		if strings.HasPrefix(a, "-") {
			if goBuildValueFlags[a] {
				i++
			}
			continue
		}
		return i, true
	}
	return 0, false
}

// goToolName returns the index of the tool go tool runs, after go tool's
// own flags.
func goToolName(args []string, lits []bool) (int, bool) {
	for i, a := range args {
		if !lits[i] {
			return 0, false
		}
		if !strings.HasPrefix(a, "-") {
			return i, true
		}
	}
	return 0, false
}

// isYtifTool reports whether go tool's argument names ytif, by its short
// name or its package path.
func isYtifTool(name string) bool {
	return name == "ytif" || strings.HasSuffix(name, "/cmd/ytif")
}

// RailCommand returns the ytif command cmd starts, through the ytif
// executable, go tool ytif, or go run of its main package, or "".
func RailCommand(cmd shellcmd.Command) string {
	next := 1
	switch cmd.NameBase() {
	case "ytif":
	case "go":
		i, ok := goSubcommand(cmd)
		if !ok || i >= len(cmd.Args) {
			return ""
		}
		if cmd.Args[i] == "tool" {
			j, ok := goToolName(cmd.Args[i+1:], cmd.Literal[i+1:])
			if !ok || !isYtifTool(cmd.Args[i+1+j]) {
				return ""
			}
			next = i + 2 + j
			break
		}
		if cmd.Args[i] != "run" {
			return ""
		}
		j, ok := goRunPackage(cmd.Args[i+1:], cmd.Literal[i+1:])
		if !ok {
			return ""
		}
		next = i + 1 + j
		bare, _, _ := strings.Cut(cmd.Args[next], "@")
		if !strings.HasSuffix(bare, "/cmd/ytif") && bare != "cmd/ytif" {
			return ""
		}
		next++
	default:
		return ""
	}
	if next >= len(cmd.Args) {
		return ""
	}
	return cmd.Args[next]
}

// inlineFlags take code to run instead of a file. Inline code belongs to
// the gate configuration itself and runs untracked, as any inline check.
var inlineFlags = map[string]map[string]bool{
	"node":   {"-e": true, "--eval": true, "-p": true, "--print": true},
	"python": {"-c": true},
	"ruby":   {"-e": true},
	"perl":   {"-e": true, "-E": true},
	"php":    {"-r": true},
	"pwsh":   {"-c": true, "-Command": true, "-command": true, "-EncodedCommand": true, "-ec": true},
}

func interpreterFamily(name string) string {
	switch {
	case name == "tsx" || name == "ts-node" || name == "bun":
		return "node"
	case strings.HasPrefix(name, "python"):
		return "python"
	case name == "powershell":
		return "pwsh"
	}
	return name
}

// interpreter classifies a language runtime by what it loads. The first
// path among its words names the program: one in the repository is
// repository code, as is any repository file an option loads. Words that
// are not paths may be option values and are passed over.
func (c *classifier) interpreter(cmd shellcmd.Command, start int, sc scope) (class, string) {
	family := interpreterFamily(path.Base(cmd.Name()))
	for i := start; i < len(cmd.Args); i++ {
		if !cmd.Literal[i] {
			return unknown, "an interpreter argument is not literal"
		}
		a := cmd.Args[i]
		switch {
		case inlineFlags[family][a]:
			return external, ""
		case family == "python" && strings.HasPrefix(a, "-m"):
			mod := a[2:]
			if mod == "" {
				if i+1 >= len(cmd.Args) || !cmd.Literal[i+1] {
					return unknown, "the python module is not literal"
				}
				mod = cmd.Args[i+1]
			}
			if c.pythonModule(mod, sc) {
				return repoCode, "runs a repository module"
			}
			return external, ""
		case strings.HasPrefix(a, "-") && a != "-":
			if _, v, ok := strings.Cut(a, "="); ok && c.place(v, sc) == inRepo {
				return repoCode, "loads a repository file"
			}
			continue
		}
		switch c.place(a, sc) {
		case inRepo:
			return repoCode, "runs a repository file"
		case installed, outside:
			return external, ""
		}
	}
	if f := cmd.Stdin.File; f != "" && c.place(f, sc) == inRepo {
		return repoCode, "runs a repository file from standard input"
	}
	return external, ""
}

// pythonModule reports whether python -m MOD resolves to a repository
// module or package from the working directory.
func (c *classifier) pythonModule(mod string, sc scope) bool {
	p := strings.ReplaceAll(mod, ".", "/")
	candidates := []string{p + ".py", p + "/__init__.py", p}
	if !sc.known {
		for _, f := range c.tracked {
			for _, cand := range candidates {
				if f == cand || strings.HasSuffix(f, "/"+cand) || strings.HasPrefix(f, p+"/") || strings.Contains(f, "/"+p+"/") {
					return true
				}
			}
		}
		return false
	}
	for _, cand := range candidates {
		if _, err := os.Stat(filepath.Join(c.root, filepath.FromSlash(sc.dir), filepath.FromSlash(cand))); err == nil {
			return true
		}
	}
	return false
}

// node classifies node: --run runs a package script; otherwise node is an
// interpreter. TestRunner has already caught --test.
func (c *classifier) node(cmd shellcmd.Command, sc scope) (class, string) {
	for i := 1; i < len(cmd.Args) && cmd.Literal[i] && strings.HasPrefix(cmd.Args[i], "-"); i++ {
		if a := cmd.Args[i]; a == "--run" || strings.HasPrefix(a, "--run=") {
			return repoCode, "runs a package script"
		}
	}
	return c.interpreter(cmd, 1, sc)
}

// operand returns the index of the first word from start that is not an
// option or the separate value of an option in values. ok is false when a
// word it passes over is not literal.
func operand(cmd shellcmd.Command, start int, values map[string]bool) (int, bool) {
	for i := start; i < len(cmd.Args); i++ {
		if !cmd.Literal[i] {
			return i, false
		}
		a := cmd.Args[i]
		if a == "--" {
			return i + 1, i+1 >= len(cmd.Args) || cmd.Literal[i+1]
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			return i, true
		}
		if values[a] {
			i++
		}
	}
	return len(cmd.Args), true
}

var bunValueFlags = set("--cwd", "-c", "--config", "--env-file", "-r", "--preload", "-d", "--define", "-l", "--loader",
	"--tsconfig-override", "--conditions", "--main-fields", "--extension-order", "--port", "--elide-lines", "-F", "--filter")

var bunBuiltins = set("install", "i", "add", "a", "remove", "rm", "update", "upgrade", "pm", "link", "unlink", "create", "c",
	"init", "build", "outdated", "publish", "patch", "patch-commit", "audit", "info", "why", "completions")

// bun classifies bun: x runs a package binary, a builtin is external, and
// anything else runs a package script or a file. TestRunner has already
// caught bun test; inline code runs untracked.
func (c *classifier) bun(cmd shellcmd.Command, sc scope) (class, string) {
	i, ok := bunSubcommand(cmd)
	if !ok {
		return unknown, "a bun argument is not literal"
	}
	if i >= len(cmd.Args) {
		return external, ""
	}
	switch sub := cmd.Args[i]; {
	case sub == "x":
		return c.execWrapper(cmd, i+1, sc)
	case bunBuiltins[sub]:
		return external, ""
	case sub != "run" && (c.place(sub, sc) == installed || c.place(sub, sc) == outside):
		return external, ""
	}
	return repoCode, "runs a package script or repository file"
}

// deno classifies deno: test, bench, and task run repository code; run and
// serve load a program like an interpreter.
func (c *classifier) deno(cmd shellcmd.Command, sc scope) (class, string) {
	i, ok := operand(cmd, 1, nil)
	if !ok {
		return unknown, "a deno argument is not literal"
	}
	if i >= len(cmd.Args) {
		return external, ""
	}
	switch sub := cmd.Args[i]; sub {
	case "test", "bench", "task":
		return repoCode, "runs repository code with deno"
	case "run", "serve":
		return c.interpreter(cmd, i+1, sc)
	case "eval":
		return external, ""
	default:
		if c.place(sub, sc) == inRepo {
			return repoCode, "runs a repository file"
		}
	}
	return external, ""
}

var npmValueFlags = set("--prefix", "-w", "--workspace", "--userconfig", "--globalconfig", "--registry", "--cache",
	"--loglevel", "--include", "--omit", "--tag", "--otp", "--scope", "--location")

var npmScripts = set("run", "run-script", "rum", "urn", "test", "t", "tst", "start", "stop", "restart",
	"it", "install-test", "cit", "install-ci-test", "clean-install-test", "sit")

func (c *classifier) npm(cmd shellcmd.Command, sc scope) (class, string) {
	i, ok := operand(cmd, 1, npmValueFlags)
	if !ok {
		return unknown, "an npm argument is not literal"
	}
	if i >= len(cmd.Args) {
		return external, ""
	}
	switch sub := cmd.Args[i]; {
	case npmScripts[sub]:
		return repoCode, "runs a package script"
	case sub == "exec" || sub == "x":
		return c.execWrapper(cmd, i+1, sc)
	}
	return external, ""
}

var pnpmValueFlags = set("-C", "--dir", "-F", "--filter", "--filter-prod", "--workspace-concurrency", "--reporter",
	"--loglevel", "--test-pattern", "--changed-files-ignore-pattern")

var pnpmBuiltins = set("install", "i", "add", "remove", "rm", "uninstall", "un", "up", "update", "upgrade", "import", "fetch",
	"link", "ln", "unlink", "list", "ls", "outdated", "why", "audit", "licenses", "store", "config", "c", "set", "get",
	"patch", "patch-commit", "patch-remove", "publish", "pack", "prune", "rebuild", "rb", "root", "bin", "setup", "env",
	"server", "init", "deploy", "doctor", "help", "self-update", "cache", "dedupe", "approve-builds", "ignored-builds")

func (c *classifier) pnpm(cmd shellcmd.Command, sc scope) (class, string) {
	i, ok := operand(cmd, 1, pnpmValueFlags)
	if !ok {
		return unknown, "a pnpm argument is not literal"
	}
	if i >= len(cmd.Args) {
		return external, ""
	}
	switch sub := cmd.Args[i]; {
	case sub == "exec" || sub == "dlx":
		return c.execWrapper(cmd, i+1, sc)
	case pnpmBuiltins[sub]:
		return external, ""
	}
	return repoCode, "runs a package script"
}

var yarnBuiltins = set("install", "add", "remove", "up", "upgrade", "upgrade-interactive", "info", "why", "config", "set",
	"cache", "npm", "plugin", "outdated", "list", "ls", "link", "unlink", "import", "version", "login", "logout", "pack",
	"publish", "init", "dedupe", "constraints", "patch", "patch-commit", "explain", "search", "stage", "unplug", "rebuild",
	"global", "licenses", "owner", "tag", "team", "audit", "autoclean", "check", "help", "policies", "bin", "create")

var yarnValueFlags = set("--cwd")

func (c *classifier) yarn(cmd shellcmd.Command, sc scope) (class, string) {
	i, ok := operand(cmd, 1, yarnValueFlags)
	if !ok {
		return unknown, "a yarn argument is not literal"
	}
	if i >= len(cmd.Args) {
		return external, "" // installs dependencies
	}
	switch sub := cmd.Args[i]; {
	case sub == "exec" || sub == "dlx":
		return c.execWrapper(cmd, i+1, sc)
	case sub == "node":
		return c.interpreter(cmd, i+1, sc)
	case sub == "workspace" && i+2 < len(cmd.Args):
		// yarn workspace NAME COMMAND...: the command as yarn runs it
		return c.yarn(shellcmd.Command{
			Args:    append([]string{"yarn"}, cmd.Args[i+2:]...),
			Literal: append([]bool{true}, cmd.Literal[i+2:]...),
			Bases:   append([]string{""}, cmd.Bases[i+2:]...),
			Text:    cmd.Text,
		}, sc)
	case yarnBuiltins[sub]:
		return external, ""
	}
	return repoCode, "runs a package script"
}

var execValueFlags = set("-p", "--package", "-w", "--workspace", "--prefix", "-C", "--dir")

// execWrapper classifies the command that npx, npm exec, pnpm exec or dlx,
// yarn exec or dlx, and bunx run: the words from i, or the script of -c.
func (c *classifier) execWrapper(cmd shellcmd.Command, i int, sc scope) (class, string) {
	for ; i < len(cmd.Args); i++ {
		if !cmd.Literal[i] {
			return unknown, "the executed command is not literal"
		}
		a := cmd.Args[i]
		switch {
		case a == "-c" || a == "--call":
			if i+1 >= len(cmd.Args) || !cmd.Literal[i+1] {
				return unknown, "the executed script is not literal"
			}
			return c.script(cmd.Args[i+1], sc)
		case strings.HasPrefix(a, "--call="):
			return c.script(strings.TrimPrefix(a, "--call="), sc)
		case a == "--":
			if i+1 >= len(cmd.Args) {
				return external, ""
			}
			return c.command(cmd.Rest(i+1), sc)
		case strings.HasPrefix(a, "-"):
			if execValueFlags[a] {
				i++
			}
			continue
		}
		return c.command(cmd.Rest(i), sc)
	}
	return external, ""
}

func (c *classifier) dotnet(cmd shellcmd.Command, sc scope) (class, string) {
	i, ok := operand(cmd, 1, nil)
	if !ok {
		return unknown, "a dotnet argument is not literal"
	}
	if i >= len(cmd.Args) {
		return external, ""
	}
	sub := cmd.Args[i]
	switch {
	case sub == "run" || sub == "watch":
		return repoCode, "runs a repository project"
	case sub == "exec":
		if j, ok := operand(cmd, i+1, nil); ok && j < len(cmd.Args) && c.place(cmd.Args[j], sc) == inRepo {
			return repoCode, "runs a repository assembly"
		}
	case strings.HasSuffix(sub, ".dll") && c.place(sub, sc) == inRepo:
		return repoCode, "runs a repository assembly"
	}
	return external, ""
}

func set(words ...string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}
