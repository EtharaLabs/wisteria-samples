package main

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

func isTestName(name string) bool {
	if !strings.HasPrefix(name, "Test") {
		return false
	}
	if name == "Test" {
		return true
	}
	r, _ := utf8.DecodeRuneInString(name[len("Test"):])
	return !unicode.IsLower(r)
}

func isTest(fn *ast.FuncDecl) bool {
	if fn.Recv != nil || !isTestName(fn.Name.Name) || fn.Type.TypeParams != nil {
		return false
	}
	if fn.Type.Results != nil && len(fn.Type.Results.List) != 0 {
		return false
	}
	if fn.Type.Params == nil || len(fn.Type.Params.List) != 1 {
		return false
	}
	param := fn.Type.Params.List[0]
	if len(param.Names) > 1 {
		return false
	}
	pointer, ok := param.Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	switch typ := pointer.X.(type) {
	case *ast.SelectorExpr:
		return typ.Sel.Name == "T"
	case *ast.Ident:
		return typ.Name == "T"
	default:
		return false
	}
}

func discover(paths []string) ([]string, error) {
	ctx := build.Default
	ctx.CgoEnabled = false
	seen := make(map[string]bool)
	for _, path := range paths {
		active, err := ctx.MatchFile(filepath.Dir(path), filepath.Base(path))
		if err != nil {
			return nil, fmt.Errorf("match active Go file %s: %w", path, err)
		}
		if !active {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, fmt.Errorf("parse Go test file %s: %w", path, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if ok && isTest(fn) {
				seen[fn.Name.Name] = true
			}
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

func run() error {
	var paths []string
	if err := json.NewDecoder(os.Stdin).Decode(&paths); err != nil {
		return fmt.Errorf("decode test file paths: %w", err)
	}
	names, err := discover(paths)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(os.Stdout).Encode(names); err != nil {
		return fmt.Errorf("encode test identities: %w", err)
	}
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}
