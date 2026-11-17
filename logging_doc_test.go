// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package tcaasgolibrary

import (
	"flag"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"io/fs"
	"maps"
	"math"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the generated log table in docs/logging.md")

const (
	loggingDoc   = "docs/logging.md"
	tableBegin   = "<!-- BEGIN GENERATED LOG TABLE -->\n"
	tableEnd     = "<!-- END GENERATED LOG TABLE -->"
	updateHint   = "go test . -run TestLoggingReference -update"
	contractHint = "library code logs only through logr from log.FromContext(ctx) (AGENTS.md rule 5)"
)

// TestLoggingReference fails when a log call under pkg/ is missing from
// docs/logging.md, the document lists one the code no longer makes, a log call
// has no literal message, or library code logs other than through logr from
// the context. Regenerate the table with updateHint.
func TestLoggingReference(t *testing.T) {
	var calls []logCall
	fset := token.NewFileSet()
	err := filepath.WalkDir("pkg", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return err
		}
		f, err := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		v := visitor{imports: imports(f), pkg: path.Dir(filepath.ToSlash(p))}
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.SelectorExpr:
				if problem := v.forbiddenSelector(n); problem != "" {
					t.Errorf("%s: %s: %s", fset.Position(n.Pos()), problem, contractHint)
				}
			case *ast.CallExpr:
				c, isLog, problem := v.classify(n)
				if problem != "" {
					t.Errorf("%s: %s: %s", fset.Position(n.Pos()), problem, contractHint)
				} else if isLog {
					calls = append(calls, c)
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(loggingDoc)
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	pdTable, missing, stale := renderPersonalData(calls)
	for _, key := range missing {
		t.Errorf("log key %q has no entry in personalData (logging_doc_test.go): classify it", key)
	}
	for _, key := range stale {
		t.Errorf("personalData lists %q, which no log call uses: remove it", key)
	}
	updated := syncTable(t, doc, tableBegin, tableEnd, renderTable(calls))
	updated = syncTable(t, updated, pdBegin, pdEnd, pdTable)
	if updated != doc {
		if err := os.WriteFile(loggingDoc, []byte(updated), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// syncTable returns doc with the generated table between begin and end set
// to want. Without -update a difference fails the test.
func syncTable(t *testing.T, doc, begin, end, want string) string {
	t.Helper()
	i, j := strings.Index(doc, begin), strings.Index(doc, end)
	if i < 0 || j < i {
		t.Fatalf("%s: generated table markers %q missing", loggingDoc, begin)
	}
	if doc[i+len(begin):j] != want && !*update {
		t.Fatalf("%s: generated table is out of date with the code; run: %s\n--- want\n%s", loggingDoc, updateHint, want)
	}
	return doc[:i+len(begin)] + want + doc[j:]
}

const (
	pdBegin = "<!-- BEGIN GENERATED PERSONAL DATA TABLE -->\n"
	pdEnd   = "<!-- END GENERATED PERSONAL DATA TABLE -->"
)

// personalData classifies every log key by the personal-data class and the
// data subject of the tcaas-psa logging contract (chapter 3). A key that a log
// call uses and that is missing here fails TestLoggingReference, and so does
// an entry for a key no log call uses.
var personalData = map[string]struct{ class, subject, content string }{
	"<kind>":    {"tenant-pii", "tenant-user", "object name, logged under the object's lower-camel kind (for example `configMap`)"},
	"certDir":   {"none", "none", "file path of the webhook certificate directory"},
	"error":     {"tenant-pii", "tenant-user", "free text; API server errors name objects and namespaces"},
	"field":     {"none", "none", "name of a field index"},
	"gvk":       {"none", "none", "group, version and kind"},
	"name":      {"tenant-pii", "tenant-user", "object name, chosen by the tenant"},
	"namespace": {"tenant-pii", "tenant-user", "namespace name, which can hold a tenant or user name"},
}

// spreadKeys lists the keys of a key/value slice passed to a log call with
// "...", which the scanner sees as one <expr> field: pkg/ssa/applier.go logs
// target.logKV as the lower-camel kind with the object name, then
// "namespace" for namespaced kinds.
var spreadKeys = map[string][]string{"<target.logKV>": {"<kind>", "namespace"}}

// renderPersonalData renders one row per log key with its classification and
// the levels it is logged at. missing are logged keys personalData lacks,
// stale are entries no log call uses.
func renderPersonalData(calls []logCall) (table string, missing, stale []string) {
	levels := map[string][]string{}
	for _, c := range calls {
		for f := range strings.SplitSeq(c.fields, ", ") {
			keys, spread := spreadKeys[strings.Trim(f, "`")]
			if !spread {
				keys = []string{strings.Trim(f, "`")}
			}
			for _, k := range keys {
				if k != "" && !slices.Contains(levels[k], c.level) {
					levels[k] = append(levels[k], c.level)
				}
			}
		}
	}
	keys := slices.Sorted(maps.Keys(levels))
	var b strings.Builder
	b.WriteString("| Field | Class | Data subject | Levels | Content |\n|---|---|---|---|---|\n")
	for _, key := range keys {
		pd, ok := personalData[key]
		if !ok {
			missing = append(missing, key)
			continue
		}
		lv := levels[key]
		slices.SortFunc(lv, func(a, b string) int { return levelRank(a) - levelRank(b) })
		b.WriteString("| `" + key + "` | " + pd.class + " | " + pd.subject + " | " + strings.Join(lv, ", ") + " | " + pd.content + " |\n")
	}
	for _, key := range slices.Sorted(maps.Keys(personalData)) {
		if _, ok := levels[key]; !ok {
			stale = append(stale, key)
		}
	}
	return b.String(), missing, stale
}

type logCall struct {
	pkg, level, msg, fields string
}

const (
	ctrlPath      = "sigs.k8s.io/controller-runtime"
	ctrlLogPath   = "sigs.k8s.io/controller-runtime/pkg/log"
	utilRuntime   = "k8s.io/apimachinery/pkg/util/runtime"
	noLiteralHint = "log call needs a literal message (a string constant or a concatenation with one)"
)

// bypassing lists packages whose use in library code bypasses the caller's
// logger.
var bypassing = []string{"log", "log/slog", "k8s.io/klog", "k8s.io/klog/v2", "go.uber.org/zap", "github.com/go-logr/zapr"}

// imports maps each file-level package name (alias or default) to its path.
func imports(f *ast.File) map[string]string {
	m := map[string]string{}
	for _, imp := range f.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		elems := strings.Split(p, "/")
		name := elems[len(elems)-1]
		if len(elems) > 1 && len(name) > 1 && name[0] == 'v' && strings.Trim(name[1:], "0123456789") == "" {
			name = elems[len(elems)-2] // k8s.io/klog/v2 -> klog
		}
		if imp.Name != nil {
			name = imp.Name.Name
		}
		m[name] = p
	}
	return m
}

type visitor struct {
	imports map[string]string
	pkg     string
}

// pkgOf returns the import path when e is a package identifier.
func (v visitor) pkgOf(e ast.Expr) string {
	if id, ok := e.(*ast.Ident); ok {
		return v.imports[id.Name]
	}
	return ""
}

// forbiddenSelector reports package-qualified identifiers that bypass the
// context logger: the bypassing packages, the global ctrl.Log / log.Log and
// SetLogger, and utilruntime.HandleError without a context.
func (v visitor) forbiddenSelector(sel *ast.SelectorExpr) string {
	pkg, name := v.pkgOf(sel.X), sel.Sel.Name
	switch {
	case pkg == "":
		return ""
	case slices.Contains(bypassing, pkg):
		return pkg + "." + name
	case (pkg == ctrlPath || pkg == ctrlLogPath) && (name == "Log" || name == "SetLogger"):
		return pkg + "." + name
	case pkg == utilRuntime && name == "HandleError":
		return pkg + ".HandleError (use HandleErrorWithContext)"
	}
	return ""
}

// classify recognizes logr calls (x.Info, x.V(n)...Info, x.Error),
// utilruntime.HandleErrorWithContext, which logs through the logr logger in
// ctx, and x.WithValues, whose keys every later line of that logger carries
// (recorded with the message withValuesMsg). problem is set for a log call without a literal message and for direct
// output (fmt.Print*, fmt.Fprint* and Write* to os.Stdout/os.Stderr, print,
// println).
func (v visitor) classify(call *ast.CallExpr) (c logCall, isLog bool, problem string) {
	if problem = v.directOutput(call); problem != "" {
		return c, false, problem
	}
	if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "WithValues" && v.pkgOf(sel.X) == "" {
		return logCall{pkg: v.pkg, level: logrLevel(sel.X), msg: withValuesMsg, fields: strings.Join(keyFields(call.Args), ", ")}, true, ""
	}
	msgIdx, level, ok := v.logCallSpec(call)
	if !ok || len(call.Args) <= msgIdx {
		return c, false, ""
	}
	msg, ok := message(call.Args[msgIdx])
	if !ok {
		return c, false, noLiteralHint + ", got " + types.ExprString(call.Args[msgIdx])
	}
	var fields []string
	if level == "error" {
		fields = append(fields, "`error`")
	}
	fields = append(fields, keyFields(call.Args[msgIdx+1:])...)
	return logCall{pkg: v.pkg, level: level, msg: msg, fields: strings.Join(fields, ", ")}, true, ""
}

// withValuesMsg is the message recorded for a WithValues call.
const withValuesMsg = "(WithValues: fields of every later line)"

// keyFields renders the keys of a key/value argument list; a key that is not
// a string literal appears as <expr>.
func keyFields(kv []ast.Expr) []string {
	var fields []string
	for k := 0; k < len(kv); k += 2 {
		if lit, ok := kv[k].(*ast.BasicLit); ok && lit.Kind == token.STRING {
			key, _ := strconv.Unquote(lit.Value)
			fields = append(fields, "`"+key+"`")
		} else {
			fields = append(fields, "`<"+types.ExprString(kv[k])+">`")
		}
	}
	return fields
}

// directOutput reports output that bypasses any logger.
func (v visitor) directOutput(call *ast.CallExpr) string {
	if id, ok := call.Fun.(*ast.Ident); ok && (id.Name == "print" || id.Name == "println") {
		return "builtin " + id.Name
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	name, recv := sel.Sel.Name, types.ExprString(sel.X)
	if (recv == "os.Stdout" || recv == "os.Stderr") && strings.HasPrefix(name, "Write") {
		return recv + "." + name
	}
	if v.pkgOf(sel.X) != "fmt" {
		return ""
	}
	if strings.HasPrefix(name, "Print") {
		return "fmt." + name
	}
	if strings.HasPrefix(name, "Fprint") && len(call.Args) > 0 {
		if w := types.ExprString(call.Args[0]); w == "os.Stdout" || w == "os.Stderr" {
			return "fmt." + name + "(" + w + ")"
		}
	}
	return ""
}

// logCallSpec returns the message argument and level of a log call.
func (v visitor) logCallSpec(call *ast.CallExpr) (msgIdx int, level string, ok bool) {
	sel, isSel := call.Fun.(*ast.SelectorExpr)
	if !isSel {
		return 0, "", false
	}
	name := sel.Sel.Name
	switch pkg := v.pkgOf(sel.X); {
	case pkg == utilRuntime && name == "HandleErrorWithContext":
		return 2, "error", true
	case pkg != "":
		return 0, "", false
	case name == "Info":
		return 0, logrLevel(sel.X), true
	case name == "Error" && len(call.Args) > 1:
		return 1, "error", true
	}
	return 0, "", false
}

// logrLevel walks x.V(n).WithValues(...).WithName(...) receivers for V(n).
func logrLevel(e ast.Expr) string {
	for {
		call, ok := e.(*ast.CallExpr)
		if !ok {
			return "info"
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return "info"
		}
		if sel.Sel.Name == "V" && len(call.Args) == 1 {
			return "V(" + types.ExprString(call.Args[0]) + ")"
		}
		e = sel.X
	}
}

// message renders a string literal or a concatenation containing one; other
// operands appear as <expr>.
func message(e ast.Expr) (string, bool) {
	switch e := e.(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(e.Value)
		return s, err == nil
	case *ast.BinaryExpr:
		if e.Op != token.ADD {
			return "", false
		}
		l, lok := message(e.X)
		r, rok := message(e.Y)
		if !lok && !rok {
			return "", false
		}
		if !lok {
			l = "<" + types.ExprString(e.X) + ">"
		}
		if !rok {
			r = "<" + types.ExprString(e.Y) + ">"
		}
		return l + r, true
	}
	return "", false
}

// levelRank orders error, info, V(n) by n, then V(<expr>) whose verbosity is
// not a literal.
func levelRank(level string) int {
	switch level {
	case "error":
		return -2
	case "info":
		return -1
	}
	n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(level, "V("), ")"))
	if err != nil || n < 0 {
		return math.MaxInt32
	}
	return n
}

func renderTable(calls []logCall) string {
	slices.SortFunc(calls, func(a, b logCall) int {
		if c := strings.Compare(a.pkg, b.pkg); c != 0 {
			return c
		}
		if d := levelRank(a.level) - levelRank(b.level); d != 0 {
			return d
		}
		if c := strings.Compare(a.level, b.level); c != 0 {
			return c
		}
		return strings.Compare(a.msg+a.fields, b.msg+b.fields)
	})
	calls = slices.Compact(calls)
	var b strings.Builder
	b.WriteString("| Package | Level | Message | Fields |\n|---|---|---|---|\n")
	esc := strings.NewReplacer("|", `\|`).Replace
	for _, c := range calls {
		fields := c.fields
		if fields == "" {
			fields = "-"
		}
		b.WriteString("| `" + c.pkg + "` | " + c.level + " | `` " + esc(c.msg) + " `` | " + esc(fields) + " |\n")
	}
	return b.String()
}

// TestLoggingCheckClassify pins what the drift check records and rejects.
func TestLoggingCheckClassify(t *testing.T) {
	const src = `package x

import (
	"fmt"
	"os"
	sl "log/slog"

	"github.com/go-logr/logr"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	crlog "sigs.k8s.io/controller-runtime/pkg/log"
)

func f(ctx context.Context, o opts, l logr.Logger, msg string, err error) {
	o.SetLogger(l)
	crlog.FromContext(ctx).V(2).WithValues("a", 1).Info("deep", "b", 2)
	l.V(n).Info("dynamic")
	l.Error(err, "failed", "k", 1)
	utilruntime.HandleErrorWithContext(ctx, err, "handled", "k", 1)
	l.Info(msg)
	sl.Info("x")
	_ = ctrl.Log
	ctrl.SetLogger(l)
	utilruntime.HandleError(err)
	fmt.Fprintln(os.Stderr, "x")
	os.Stdout.WriteString("x")
	print("x")
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "x.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	v := visitor{imports: imports(f), pkg: "pkg/x"}
	var got []string
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.SelectorExpr:
			if p := v.forbiddenSelector(n); p != "" {
				got = append(got, "FORBIDDEN "+p)
			}
		case *ast.CallExpr:
			c, isLog, problem := v.classify(n)
			switch {
			case problem != "":
				got = append(got, "PROBLEM "+types.ExprString(n.Fun))
			case isLog:
				got = append(got, c.level+" "+c.msg+" "+c.fields)
			}
		}
		return true
	})
	want := []string{
		"V(2) deep `b`",
		"V(2) " + withValuesMsg + " `a`",
		"V(n) dynamic ",
		"error failed `error`, `k`",
		"error handled `error`, `k`",
		"PROBLEM l.Info",
		"FORBIDDEN log/slog.Info",
		"FORBIDDEN sigs.k8s.io/controller-runtime.Log",
		"FORBIDDEN sigs.k8s.io/controller-runtime.SetLogger",
		"FORBIDDEN k8s.io/apimachinery/pkg/util/runtime.HandleError (use HandleErrorWithContext)",
		"PROBLEM fmt.Fprintln",
		"PROBLEM os.Stdout.WriteString",
		"PROBLEM print",
	}
	if !slices.Equal(got, want) {
		t.Errorf("classify:\n got %q\nwant %q", got, want)
	}
	if levelRank("V(n)") <= levelRank("V(3)") {
		t.Error("V(<expr>) must sort after literal verbosities")
	}
}
