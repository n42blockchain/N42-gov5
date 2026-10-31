package evmsdk

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"strings"
	"testing"
)

// gomobile binds this package for the phones (make android, make ios), and
// gobind refuses the whole package, not just the one function, when an
// exported package function returns more than one value besides an error.
// (A method it cannot bind it skips.) gobind is not a dependency of the
// repository, so the rule is checked on the source.
func TestExportedFunctionsAreBindable(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok || fd.Recv != nil || !fd.Name.IsExported() || fd.Type.Results == nil {
					continue
				}
				var results []ast.Expr
				for _, f := range fd.Type.Results.List {
					for range max(len(f.Names), 1) {
						results = append(results, f.Type)
					}
				}
				last, _ := results[len(results)-1].(*ast.Ident)
				if len(results) > 2 || (len(results) == 2 && (last == nil || last.Name != "error")) {
					t.Errorf("%s: %s returns %d values; gobind takes one value and optionally an error",
						fset.Position(fd.Pos()), fd.Name.Name, len(results))
				}
			}
		}
	}
}

// The session is what the app calls; every method of it must reach Java
// and Swift, and gobind silently drops a method it cannot bind. So its
// API, and its constructor's, takes and returns strings, int64s, bools
// and errors only.
func TestSessionAPIIsBindable(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "mobileverify_session.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"string": true, "int64": true, "bool": true, "error": true}
	methods := 0
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || !fd.Name.IsExported() {
			continue
		}
		var fields []*ast.Field
		fields = append(fields, fd.Type.Params.List...)
		if fd.Type.Results != nil {
			fields = append(fields, fd.Type.Results.List...)
		}
		for _, f := range fields {
			typ := f.Type
			if star, ok := typ.(*ast.StarExpr); ok && fd.Recv == nil {
				typ = star.X // the constructor returns *MobileVerifySession
				if id, ok := typ.(*ast.Ident); ok && id.Name == "MobileVerifySession" {
					continue
				}
			}
			if id, ok := typ.(*ast.Ident); !ok || !allowed[id.Name] {
				t.Errorf("%s: %s uses a type gobind does not bind for the session", fset.Position(f.Pos()), fd.Name.Name)
			}
		}
		if fd.Recv != nil {
			methods++
		}
	}
	if methods < 8 {
		t.Fatalf("found %d session methods, want the 8 the app calls", methods)
	}
}
