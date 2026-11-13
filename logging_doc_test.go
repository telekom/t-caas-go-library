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
	i, j := strings.Index(doc, tableBegin), strings.Index(doc, tableEnd)
	if i < 0 || j < i {
		t.Fatalf("%s: generated table markers missing", loggingDoc)
	}
	want := renderTable(calls)
	if doc[i+len(tableBegin):j] == want {
		return
	}
	if !*update {
		t.Fatalf("%s: log table is out of date with the code; run: %s\n--- want\n%s", loggingDoc, updateHint, want)
	}
	doc = doc[:i+len(tableBegin)] + want + doc[j:]
	if err := os.WriteFile(loggingDoc, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
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

// classify recognizes logr calls (x.Info, x.V(n)...Info, x.Error) and
// utilruntime.HandleErrorWithContext, which logs through the logr logger in
// ctx. problem is set for a log call without a literal message and for direct
// output (fmt.Print*, fmt.Fprint* and Write* to os.Stdout/os.Stderr, print,
// println).
func (v visitor) classify(call *ast.CallExpr) (c logCall, isLog bool, problem string) {
	if problem = v.directOutput(call); problem != "" {
		return c, false, problem
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
	for k := msgIdx + 1; k < len(call.Args); k += 2 {
		if lit, ok := call.Args[k].(*ast.BasicLit); ok && lit.Kind == token.STRING {
			key, _ := strconv.Unquote(lit.Value)
			fields = append(fields, "`"+key+"`")
		} else {
			fields = append(fields, "`<"+types.ExprString(call.Args[k])+">`")
		}
	}
	return logCall{pkg: v.pkg, level: level, msg: msg, fields: strings.Join(fields, ", ")}, true, ""
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
