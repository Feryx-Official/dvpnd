// SPDX-License-Identifier: Apache-2.0

package static

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Feryx-Official/dvpnd/v9/services"
)

// callsOn reports whether n calls recv.method (service.Stop), outside any
// function literal: a literal runs later, or never.
func callsOn(n ast.Node, recv, method string) bool {
	return calls(n, recv, method, false)
}

// calls is callsOn, optionally looking into function literals too (the
// closure a defer runs).
func calls(n ast.Node, recv, method string, intoLiterals bool) bool {
	found := false
	ast.Inspect(n, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncLit:
			return intoLiterals
		case *ast.CallExpr:
			if sel, ok := n.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == method {
				if id, ok := sel.X.(*ast.Ident); ok && id.Name == recv {
					found = true
				}
			}
		}
		return !found
	})

	return found
}

// unstoppedReturns lists the returns in stmts that leave without stopping the
// service: no deferred service.Stop before them, and no service.Stop call
// earlier on their path.
func unstoppedReturns(fset *token.FileSet, stmts []ast.Stmt, stopped bool, deferred *bool) []string {
	var bad []string
	for _, s := range stmts {
		switch s := s.(type) {
		case *ast.DeferStmt:
			if calls(s.Call, "service", "Stop", true) {
				*deferred = true
			}
		case *ast.ReturnStmt:
			if !stopped && !*deferred {
				bad = append(bad, fset.Position(s.Pos()).String())
			}
		case *ast.IfStmt:
			head := stopped || (s.Init != nil && callsOn(s.Init, "service", "Stop")) || callsOn(s.Cond, "service", "Stop")
			bad = append(bad, unstoppedReturns(fset, s.Body.List, head, deferred)...)
			if s.Else != nil {
				bad = append(bad, unstoppedReturns(fset, []ast.Stmt{s.Else}, head, deferred)...)
			}
			stopped = head
		case *ast.BlockStmt:
			bad = append(bad, unstoppedReturns(fset, s.List, stopped, deferred)...)
		case *ast.ForStmt:
			bad = append(bad, unstoppedReturns(fset, s.Body.List, stopped, deferred)...)
		case *ast.RangeStmt:
			bad = append(bad, unstoppedReturns(fset, s.Body.List, stopped, deferred)...)
		case *ast.SwitchStmt:
			bad = append(bad, unstoppedReturns(fset, s.Body.List, stopped, deferred)...)
		case *ast.TypeSwitchStmt:
			bad = append(bad, unstoppedReturns(fset, s.Body.List, stopped, deferred)...)
		case *ast.SelectStmt:
			bad = append(bad, unstoppedReturns(fset, s.Body.List, stopped, deferred)...)
		case *ast.CaseClause:
			bad = append(bad, unstoppedReturns(fset, s.Body, stopped, deferred)...)
		case *ast.CommClause:
			bad = append(bad, unstoppedReturns(fset, s.Body, stopped, deferred)...)
		default:
			if callsOn(s, "service", "Stop") {
				stopped = true
			}
		}
	}

	return bad
}

// TestStartStopsTheServiceOnEveryExit reads the start command: once
// service.Start() has succeeded, every return must come after a deferred
// service.Stop or after a service.Stop call on its path, so no step that fails
// later leaves the tunnel, the firewall rules or the daemon behind.
//
// Rules: [RT-2].
func TestStartStopsTheServiceOnEveryExit(t *testing.T) {
	fset, body, start := startCommand(t)
	deferred := false
	for _, pos := range unstoppedReturns(fset, body.List[start+1:], false, &deferred) {
		t.Errorf("%s returns after service.Start() without stopping the service", pos)
	}
}

// TestStartCatchesAStopSignalOnceTheServiceRuns reads the start command: the
// stop signals are caught from before service.Start(). Left to their default
// action, a SIGTERM while the node still started (opening the database, going
// active on the chain, reconciling the sessions) ended the process at once,
// with no deferred call run, and the tunnel and the firewall rules stayed.
//
// Rules: [RT-2].
func TestStartCatchesAStopSignalOnceTheServiceRuns(t *testing.T) {
	_, body, start := startCommand(t)
	for i, s := range body.List {
		if callsOn(s, "signal", "Notify") {
			if i > start {
				t.Errorf("signal.Notify comes after service.Start(): a stop signal in between kills the node without stopping the service")
			}

			return
		}
	}
	t.Error("the start command never catches a stop signal (no signal.Notify in RunE)")
}

// startCommand returns the body of the start command's RunE and the index of
// the statement in it that calls service.Start().
func startCommand(t *testing.T) (*token.FileSet, *ast.BlockStmt, int) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filepath.Join(root(t), "cmd", "start.go"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var body *ast.BlockStmt
	ast.Inspect(file, func(n ast.Node) bool {
		if kv, ok := n.(*ast.KeyValueExpr); ok {
			if id, ok := kv.Key.(*ast.Ident); ok && id.Name == "RunE" {
				if fl, ok := kv.Value.(*ast.FuncLit); ok {
					body = fl.Body
				}
			}
		}
		return body == nil
	})
	if body == nil {
		t.Fatal("no RunE function literal in cmd/start.go: re-aim this test at the start command")
	}

	start := -1
	for i, s := range body.List {
		if callsOn(s, "service", "Start") {
			if start >= 0 {
				t.Fatal("service.Start() is called twice: re-aim this test")
			}
			start = i
		}
	}
	if start < 0 {
		t.Fatal("no service.Start() in the start command: re-aim this test")
	}

	return fset, body, start
}

// caseNames returns the name of each element of the slice literal the named
// function returns: the protocols the integration suite runs.
func caseNames(t *testing.T, file, fn string) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(root(t), file), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Name.Name != fn {
			continue
		}
		for _, s := range fd.Body.List {
			ret, ok := s.(*ast.ReturnStmt)
			if !ok || len(ret.Results) != 1 {
				continue
			}
			lit, ok := ret.Results[0].(*ast.CompositeLit)
			if !ok {
				continue
			}
			for _, e := range lit.Elts {
				el, ok := e.(*ast.CompositeLit)
				if !ok {
					continue
				}
				for _, kv := range el.Elts {
					kv, ok := kv.(*ast.KeyValueExpr)
					if id, isID := kv.Key.(*ast.Ident); ok && isID && id.Name == "name" {
						if bl, ok := kv.Value.(*ast.BasicLit); ok {
							v, _ := strconv.Unquote(bl.Value)
							names = append(names, v)
						}
					}
				}
			}
		}
	}
	if len(names) == 0 {
		t.Fatalf("no cases in %s's %s: re-aim this test", file, fn)
	}

	return names
}

// Rules: [EG-13].
func TestEveryProtocolIsInTheIntegrationSuite(t *testing.T) {
	cases := append(caseNames(t, "test/integration/proxies_test.go", "proxyCases"),
		caseNames(t, "test/integration/tunnels_test.go", "tunnelCases")...)
	for _, name := range services.Names() {
		covered := false
		for _, c := range cases {
			if c == name || strings.HasPrefix(c, name+"-") {
				covered = true
			}
		}
		if !covered {
			t.Errorf("protocol %q has no case in the integration suite (proxyCases or tunnelCases)", name)
		}
	}
}

// TestUnstoppedReturnsChecker holds the checker above to known shapes, so a
// checker that sees nothing cannot pass the start command.
func TestUnstoppedReturnsChecker(t *testing.T) {
	cases := []struct {
		name string
		body string
		bad  int
	}{
		{"a return before any stop", `if err != nil { return err }; service.Stop(); return nil`, 1},
		{"a deferred stop covers every later return", `defer service.Stop(); if err != nil { return err }; return nil`, 0},
		{"a deferred closure counts", `defer func() { _ = service.Stop() }(); return nil`, 0},
		{"a stop in an if's init covers what follows", `if err := service.Stop(); err != nil { return err }; return nil`, 0},
		{"another receiver's Stop does not count", `defer signal.Stop(ch); return nil`, 1},
		{"a stop inside a function literal does not count", `go func() { service.Stop() }(); return nil`, 1},
	}
	for _, c := range cases {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, "x.go", "package x\nfunc f() error {"+c.body+"}", 0)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		deferred := false
		if got := unstoppedReturns(fset, f.Decls[0].(*ast.FuncDecl).Body.List, false, &deferred); len(got) != c.bad {
			t.Errorf("%s: %d unstopped returns %v, want %d", c.name, len(got), got, c.bad)
		}
	}
}

// databaseOpens lists where a Go file calls Open from gorm or its sqlite
// driver, whatever name it imports them under.
func databaseOpens(t *testing.T, fset *token.FileSet, rel string) []string {
	t.Helper()
	file, err := parser.ParseFile(fset, filepath.Join(root(t), rel), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	pkgs := map[string]bool{}
	for _, imp := range file.Imports {
		path, _ := strconv.Unquote(imp.Path.Value)
		if path != "gorm.io/gorm" && path != "gorm.io/driver/sqlite" {
			continue
		}
		name := path[strings.LastIndex(path, "/")+1:]
		if imp.Name != nil {
			name = imp.Name.Name
		}
		pkgs[name] = true
	}
	var found []string
	ast.Inspect(file, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Open" {
				if id, ok := sel.X.(*ast.Ident); ok && pkgs[id.Name] {
					found = append(found, fset.Position(call.Pos()).String())
				}
			}
		}
		return true
	})

	return found
}

// TestOneDoorToTheDatabase: the node opens its session database in one
// place, node.OpenDatabase, so the way it is opened (secure_delete) is the
// way the tests open it.
//
// Rules: [PV-4].
func TestOneDoorToTheDatabase(t *testing.T) {
	const door = "node/database.go"
	fset := token.NewFileSet()
	if len(databaseOpens(t, fset, door)) == 0 {
		t.Fatalf("%s opens no database: re-aim this test at the function that does", door)
	}
	for _, f := range files(t, "*.go") {
		if f == door || strings.HasSuffix(f, "_test.go") {
			continue
		}
		for _, pos := range databaseOpens(t, fset, f) {
			t.Errorf("%s opens a database outside %s: call node.OpenDatabase instead", pos, door)
		}
	}
}

// TestOneLoopOverTheRemotes: the chain client reads its list of RPC remotes
// in one place, the loop that tries each in turn, so queries, broadcasts and
// gas estimates cannot drift from what the loop's test checks.
//
// Rules: [CH-8].
func TestOneLoopOverTheRemotes(t *testing.T) {
	fset := token.NewFileSet()
	readers := map[string]int{}
	for _, f := range files(t, "lite/*.go") {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(root(t), f), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range file.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok {
				continue
			}
			ast.Inspect(fd, func(n ast.Node) bool {
				if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "remotes" {
					readers[fd.Name.Name]++
				}
				return true
			})
		}
	}
	delete(readers, "WithRemotes") // the setter
	if len(readers) != 1 || readers["eachRemote"] != 1 {
		t.Fatalf("the remote list is read by %v; only eachRemote may loop over it", readers)
	}
}

// TestOneLoopForTheJobs: the node's periodic work runs in one loop, runJob,
// whose test checks that a failed pass is tried again; no job keeps a ticker,
// a timer or a sleep of its own, whose loop could end on an error.
//
// Rules: [SL-12].
func TestOneLoopForTheJobs(t *testing.T) {
	waits := map[string]bool{"NewTicker": true, "Tick": true, "NewTimer": true, "After": true, "AfterFunc": true, "Sleep": true}
	fset := token.NewFileSet()
	clocks := map[string][]string{}
	for _, f := range files(t, "node/*.go") {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(root(t), f), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range file.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok {
				continue
			}
			ast.Inspect(fd, func(n ast.Node) bool {
				if sel, ok := n.(*ast.SelectorExpr); ok && waits[sel.Sel.Name] {
					if id, ok := sel.X.(*ast.Ident); ok && id.Name == "time" {
						clocks[fd.Name.Name] = append(clocks[fd.Name.Name], fset.Position(sel.Pos()).String())
					}
				}
				return true
			})
		}
	}
	if len(clocks["runJob"]) == 0 {
		t.Fatal("runJob keeps no ticker: re-aim this test at the loop that runs the jobs")
	}
	delete(clocks, "runJob")
	for fn, at := range clocks {
		t.Errorf("%s waits on a clock of its own at %v: run it as a job through runJob", fn, at)
	}
}
