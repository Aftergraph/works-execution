package workgraph

// Registration guard for the State vocabulary.
//
// Go has no runtime reflection over typed string constants: there is no
// way to enumerate the members of `type State string` from a test in
// another package. That is why TestStateVocabularyFrozen (workgraph_test)
// can only assert that AllStates() matches a list it was given — a check
// that a 15th constant, registered or not, cannot fail on its own.
//
// This file closes the remaining direction by reading the package's OWN
// source. It parses every non-test .go file in this directory with
// stdlib go/parser, collects every constant explicitly typed as State,
// and requires each one to appear in AllStates().
//
// The consequence is the property the freeze is supposed to have: adding
// a State requires editing BOTH the constant block and AllStates(). A
// constant declared and forgotten fails here. A state listed in AllStates()
// without a matching constant fails TestStateVocabularyFrozen's
// membership set. Neither gap can be walked through.
//
// If this test starts failing because the package moved or split, the
// cause is a source layout change, not a vocabulary change: extend the
// file scan rather than deleting the guard.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestStateConstantsAreRegistered fails when a State-typed constant is
// declared in this package but is missing from AllStates().
func TestStateConstantsAreRegistered(t *testing.T) {
	declared, err := parseStateConstants(".")
	if err != nil {
		t.Fatalf("parse package sources: %v", err)
	}
	if len(declared) == 0 {
		t.Fatal("no State-typed constants found — the source scan is broken, so it is guarding nothing")
	}

	registered := map[State]bool{}
	for _, s := range AllStates() {
		registered[s] = true
	}

	// Name -> value, so the failure message can name the offending const.
	names := map[State]string{}
	for _, c := range declared {
		if !registered[c.value] {
			t.Errorf("constant %s = %q is declared but not registered in AllStates() — "+
				"every State constant must appear in the authoritative vocabulary",
				c.name, string(c.value))
		}
		if prev, dup := names[c.value]; dup {
			t.Errorf("constants %s and %s both declare %q — the vocabulary must be distinct", prev, c.name, string(c.value))
		}
		names[c.value] = c.name
	}

	// The mirror image: AllStates() entries with no constant behind them
	// mean the vocabulary list drifted from the source of truth.
	declaredSet := map[State]bool{}
	for _, c := range declared {
		declaredSet[c.value] = true
	}
	for _, s := range AllStates() {
		if !declaredSet[s] {
			t.Errorf("AllStates() lists %q but no State-typed constant declares it — remove it or declare the constant", s)
		}
	}
}

type stateConst struct {
	name  string
	value State
}

// parseStateConstants walks every non-test .go file in dir and returns
// each constant explicitly typed as State, with its string literal value.
//
// Only explicitly-typed declarations are collected. This package declares
// every State constant as `Name State = "VALUE"`; a hypothetical untyped
// or iota-built constant would not be visible here, which is exactly why
// the vocabulary is also pinned by count in TestStateVocabularyFrozen.
func parseStateConstants(dir string) ([]stateConst, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []stateConst
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			return nil, err
		}
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				if !isIdentNamed(vs.Type, "State") {
					continue
				}
				if len(vs.Names) != len(vs.Values) {
					// A grouped/derived declaration (no explicit value per
					// name). Not present in this package; skip rather than
					// guess at its value.
					continue
				}
				for i, ident := range vs.Names {
					lit, ok := vs.Values[i].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					unquoted, err := strconv.Unquote(lit.Value)
					if err != nil {
						return nil, err
					}
					out = append(out, stateConst{name: ident.Name, value: State(unquoted)})
				}
			}
		}
	}
	return out, nil
}

func isIdentNamed(e ast.Expr, name string) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == name
}