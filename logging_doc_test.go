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
// docs/logging.md, the document lists one the code no longer makes, or library
// code logs other than through logr. Regenerate the table with updateHint.
func TestLoggingReference(t *testing.T) {
	var calls []logCall
	fset := token.NewFileSet()
	err := filepath.WalkDir("pkg", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return err
		}
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if c, ok := classify(call); ok {
				c.pkg = path.Dir(filepath.ToSlash(p))
				calls = append(calls, c)
			} else if forbidden(call) {
				t.Errorf("%s: %s: %s", fset.Position(call.Pos()), types.ExprString(call.Fun), contractHint)
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

// classify recognizes logr calls (x.Info, x.V(n).Info, x.Error) and
// utilruntime.HandleErrorWithContext, which logs through the logr logger in
// ctx, with a string (or string concatenation) message.
func classify(call *ast.CallExpr) (logCall, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return logCall{}, false
	}
	msgIdx, level := 0, "info"
	switch sel.Sel.Name {
	case "HandleErrorWithContext":
		msgIdx, level = 2, "error"
	case "Info":
		if v, ok := sel.X.(*ast.CallExpr); ok && len(v.Args) == 1 {
			if vs, ok := v.Fun.(*ast.SelectorExpr); ok && vs.Sel.Name == "V" {
				level = "V(" + types.ExprString(v.Args[0]) + ")"
			}
		}
	case "Error":
		msgIdx, level = 1, "error"
	default:
		return logCall{}, false
	}
	if len(call.Args) <= msgIdx {
		return logCall{}, false
	}
	msg, ok := message(call.Args[msgIdx])
	if !ok {
		return logCall{}, false
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
	return logCall{level: level, msg: msg, fields: strings.Join(fields, ", ")}, true
}

// forbidden reports output that bypasses the caller's logr logger.
func forbidden(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	recv, name := types.ExprString(sel.X), sel.Sel.Name
	if name == "SetLogger" {
		return true
	}
	switch recv {
	case "slog", "klog":
		return true
	case "log":
		return strings.HasPrefix(name, "Print") || strings.HasPrefix(name, "Fatal") || strings.HasPrefix(name, "Panic")
	case "fmt":
		if strings.HasPrefix(name, "Print") {
			return true
		}
		if strings.HasPrefix(name, "Fprint") && len(call.Args) > 0 {
			w := types.ExprString(call.Args[0])
			return w == "os.Stdout" || w == "os.Stderr"
		}
	}
	return false
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

// levelRank orders error, info, then V(n) by n.
func levelRank(level string) int {
	switch level {
	case "error":
		return -2
	case "info":
		return -1
	}
	n, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(level, "V("), ")"))
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
