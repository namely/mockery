package mockery

import (
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/build"
	"go/types"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"golang.org/x/tools/imports"
)

var invalidIdentifierChar = regexp.MustCompile("[^[:digit:][:alpha:]_]")

// Generator is responsible for generating the string containing
// imports and the mock struct that will later be written out as file.
type Generator struct {
	buf bytes.Buffer

	ip    bool
	iface *Interface
	pkg   string

	importsWerePopulated bool
	localizationCache    map[string]string
	packagePathToName    map[string]string
	nameToPackagePath    map[string]string

	packageRoots []string
}

// NewGenerator builds a Generator.
func NewGenerator(iface *Interface, pkg string, inPackage bool) *Generator {
	var roots []string

	for _, root := range filepath.SplitList(build.Default.GOPATH) {
		roots = append(roots, filepath.Join(root, "src"))
	}

	g := &Generator{
		iface:             iface,
		pkg:               pkg,
		ip:                inPackage,
		localizationCache: make(map[string]string),
		packagePathToName: make(map[string]string),
		nameToPackagePath: make(map[string]string),
		packageRoots:      roots,
	}

	g.addPackageImportWithName("github.com/stretchr/testify/mock", "mock")
	return g
}

func (g *Generator) populateImports() {
	if g.importsWerePopulated {
		return
	}
	for i := 0; i < g.iface.Type.NumMethods(); i++ {
		fn := g.iface.Type.Method(i)
		ftype := fn.Type().(*types.Signature)
		g.addImportsFromTuple(ftype.Params())
		g.addImportsFromTuple(ftype.Results())
		g.renderType(g.iface.NamedType)
	}
}

func (g *Generator) addImportsFromTuple(list *types.Tuple) {
	for i := 0; i < list.Len(); i++ {
		// We use renderType here because we need to recursively
		// resolve any types to make sure that all named types that
		// will appear in the interface file are known
		g.renderType(list.At(i).Type())
	}
}

func (g *Generator) addPackageImport(pkg *types.Package) string {
	return g.addPackageImportWithName(pkg.Path(), pkg.Name())
}

func (g *Generator) addPackageImportWithName(path, name string) string {
	path = g.getLocalizedPath(path)
	if existingName, pathExists := g.packagePathToName[path]; pathExists {
		return existingName
	}

	nonConflictingName := g.getNonConflictingName(path, name)
	g.packagePathToName[path] = nonConflictingName
	g.nameToPackagePath[nonConflictingName] = path
	return nonConflictingName
}

func (g *Generator) getNonConflictingName(path, name string) string {
	if !g.importNameExists(name) {
		return name
	}

	// The path will always contain '/' because it is enforced in getLocalizedPath
	// regardless of OS.
	directories := strings.Split(path, "/")

	cleanedDirectories := make([]string, 0, len(directories))
	for _, directory := range directories {
		cleaned := invalidIdentifierChar.ReplaceAllString(directory, "_")
		cleanedDirectories = append(cleanedDirectories, cleaned)
	}
	numDirectories := len(cleanedDirectories)
	var prospectiveName string
	for i := 1; i <= numDirectories; i++ {
		prospectiveName = strings.Join(cleanedDirectories[numDirectories-i:], "")
		if !g.importNameExists(prospectiveName) {
			return prospectiveName
		}
	}
	// Try adding numbers to the given name
	i := 2
	for {
		prospectiveName = fmt.Sprintf("%v%d", name, i)
		if !g.importNameExists(prospectiveName) {
			return prospectiveName
		}
		i++
	}
}

func (g *Generator) importNameExists(name string) bool {
	_, nameExists := g.nameToPackagePath[name]
	return nameExists
}

func calculateImport(set []string, path string) string {
	for _, root := range set {
		if strings.HasPrefix(path, root) {
			packagePath, err := filepath.Rel(root, path)
			if err == nil {
				return packagePath
			} else {
				log.Printf("Unable to localize path %v, %v", path, err)
			}
		}
	}
	return path
}

// TODO(@IvanMalison): Is there not a better way to get the actual
// import path of a package?
func (g *Generator) getLocalizedPath(path string) string {
	if strings.HasSuffix(path, ".go") {
		path, _ = filepath.Split(path)
	}
	if localized, ok := g.localizationCache[path]; ok {
		return localized
	}
	directories := strings.Split(path, string(filepath.Separator))
	numDirectories := len(directories)
	vendorIndex := -1
	for i := 1; i <= numDirectories; i++ {
		dir := directories[numDirectories-i]
		if dir == "vendor" {
			vendorIndex = numDirectories - i
			break
		}
	}

	toReturn := path
	if vendorIndex >= 0 {
		toReturn = filepath.Join(directories[vendorIndex+1:]...)
	} else if filepath.IsAbs(path) {
		toReturn = calculateImport(g.packageRoots, path)
	}

	// Enforce '/' slashes for import paths in every OS.
	toReturn = filepath.ToSlash(toReturn)

	g.localizationCache[path] = toReturn
	return toReturn
}

func (g *Generator) mockName() string {
	if g.ip {
		if ast.IsExported(g.iface.Name) {
			return "Mock" + g.iface.Name
		}
		first := true
		return "mock" + strings.Map(func(r rune) rune {
			if first {
				first = false
				return unicode.ToUpper(r)
			}
			return r
		}, g.iface.Name)
	}

	return g.iface.Name
}

func (g *Generator) sortedImportNames() (importNames []string) {
	for name := range g.nameToPackagePath {
		importNames = append(importNames, name)
	}
	sort.Strings(importNames)
	return
}

func (g *Generator) generateImports() {
	pkgPath := g.nameToPackagePath[g.iface.Pkg.Name()]
	// Sort by import name so that we get a deterministic order
	for _, name := range g.sortedImportNames() {
		path := g.nameToPackagePath[name]
		if g.ip && path == pkgPath {
			continue
		}
		g.printf("import %s \"%s\"\n", name, path)
	}
}

// GeneratePrologue generates the prologue of the mock.
func (g *Generator) GeneratePrologue(pkg string) {
	g.populateImports()
	if g.ip {
		g.printf("package %s\n\n", g.iface.Pkg.Name())
	} else {
		g.printf("package %v\n\n", pkg)
	}

	g.generateImports()
	g.printf("\n")
}

// GeneratePrologueNote adds a note after the prologue to the output
// string.
func (g *Generator) GeneratePrologueNote(note string) {
	g.printf("// Code generated by mockery v%s. DO NOT EDIT.\n", SemVer)
	if note != "" {
		g.printf("\n")
		for _, n := range strings.Split(note, "\\n") {
			g.printf("// %s\n", n)
		}
	}
	g.printf("\n")
}

// ErrNotInterface is returned when the given type is not an interface
// type.
var ErrNotInterface = errors.New("expression not an interface")

func (g *Generator) printf(s string, vals ...interface{}) {
	fmt.Fprintf(&g.buf, s, vals...)
}

type namer interface {
	Name() string
}

func (g *Generator) renderType(typ types.Type) string {
	switch t := typ.(type) {
	case *types.Named:
		o := t.Obj()
		if o.Pkg() == nil || o.Pkg().Name() == "main" || (g.ip && o.Pkg() == g.iface.Pkg) {
			return o.Name()
		}
		return g.addPackageImport(o.Pkg()) + "." + o.Name()
	case *types.Basic:
		return t.Name()
	case *types.Pointer:
		return "*" + g.renderType(t.Elem())
	case *types.Slice:
		return "[]" + g.renderType(t.Elem())
	case *types.Array:
		return fmt.Sprintf("[%d]%s", t.Len(), g.renderType(t.Elem()))
	case *types.Signature:
		switch t.Results().Len() {
		case 0:
			return fmt.Sprintf(
				"func(%s)",
				g.renderTypeTuple(t.Params()),
			)
		case 1:
			return fmt.Sprintf(
				"func(%s) %s",
				g.renderTypeTuple(t.Params()),
				g.renderType(t.Results().At(0).Type()),
			)
		default:
			return fmt.Sprintf(
				"func(%s)(%s)",
				g.renderTypeTuple(t.Params()),
				g.renderTypeTuple(t.Results()),
			)
		}
	case *types.Map:
		kt := g.renderType(t.Key())
		vt := g.renderType(t.Elem())

		return fmt.Sprintf("map[%s]%s", kt, vt)
	case *types.Chan:
		switch t.Dir() {
		case types.SendRecv:
			return "chan " + g.renderType(t.Elem())
		case types.RecvOnly:
			return "<-chan " + g.renderType(t.Elem())
		default:
			return "chan<- " + g.renderType(t.Elem())
		}
	case *types.Struct:
		var fields []string

		for i := 0; i < t.NumFields(); i++ {
			f := t.Field(i)

			if f.Anonymous() {
				fields = append(fields, g.renderType(f.Type()))
			} else {
				fields = append(fields, fmt.Sprintf("%s %s", f.Name(), g.renderType(f.Type())))
			}
		}

		return fmt.Sprintf("struct{%s}", strings.Join(fields, ";"))
	case *types.Interface:
		if t.NumMethods() != 0 {
			panic("Unable to mock inline interfaces with methods")
		}

		return "interface{}"
	case namer:
		return t.Name()
	default:
		panic(fmt.Sprintf("un-namable type: %#v (%T)", t, t))
	}
}

func (g *Generator) renderTypeTuple(tup *types.Tuple) string {
	var parts []string

	for i := 0; i < tup.Len(); i++ {
		v := tup.At(i)

		parts = append(parts, g.renderType(v.Type()))
	}

	return strings.Join(parts, " , ")
}

func isNillable(typ types.Type) bool {
	switch t := typ.(type) {
	case *types.Pointer, *types.Array, *types.Map, *types.Interface, *types.Signature, *types.Chan, *types.Slice:
		return true
	case *types.Named:
		return isNillable(t.Underlying())
	}
	return false
}

type paramList struct {
	Names    []string
	Types    []string
	Params   []string
	Nilable  []bool
	Variadic bool
}

func (g *Generator) genList(list *types.Tuple, variadic bool) *paramList {
	var params paramList

	if list == nil {
		return &params
	}

	for i := 0; i < list.Len(); i++ {
		v := list.At(i)

		ts := g.renderType(v.Type())

		if variadic && i == list.Len()-1 {
			t := v.Type()
			switch t := t.(type) {
			case *types.Slice:
				params.Variadic = true
				ts = "..." + g.renderType(t.Elem())
			default:
				panic("bad variadic type!")
			}
		}

		pname := v.Name()

		if g.nameCollides(pname) || pname == "" {
			pname = fmt.Sprintf("_a%d", i)
		}

		params.Names = append(params.Names, pname)
		params.Types = append(params.Types, ts)

		params.Params = append(params.Params, fmt.Sprintf("%s %s", pname, ts))
		params.Nilable = append(params.Nilable, isNillable(v.Type()))
	}

	return &params
}

func (g *Generator) nameCollides(pname string) bool {
	if pname == g.pkg {
		return true
	}
	return g.importNameExists(pname)
}

// ErrNotSetup is returned when the generator is not configured.
var ErrNotSetup = errors.New("not setup")

// Generate builds a string that constitutes a valid go source file
// containing the mock of the relevant interface.
func (g *Generator) Generate() error {
	g.populateImports()
	if g.iface == nil {
		return ErrNotSetup
	}

	g.printf(
		"// %s is an autogenerated mock type for the %s type\n", g.mockName(),
		g.iface.Name,
	)

	g.printf(
		"type %s struct {\n\tmock.Mock\n}\n\n", g.mockName(),
	)

	expectationName := g.mockName() + "Expectation"
	g.printf(
		"type %s struct {\n\tmock *mock.Mock\n}\n\n", expectationName,
	)

	g.printf(
		"func (_m *%s) Expect() *%s {\n\treturn &%s{mock: &_m.Mock}\n}\n", g.mockName(), expectationName, expectationName,
	)

	for i := 0; i < g.iface.Type.NumMethods(); i++ {
		g.printf("\n")
		fn := g.iface.Type.Method(i)

		ftype := fn.Type().(*types.Signature)
		fname := fn.Name()

		params := g.genList(ftype.Params(), ftype.Variadic())
		returns := g.genList(ftype.Results(), false)

		g.mockMethod(fname, params, returns)
		g.mockMethodExpectation(expectationName, fname, params, returns)
	}

	return nil
}

func (g *Generator) mockMethod(fname string, params, returns *paramList) {
	if len(params.Names) == 0 {
		g.printf("// %s provides a mock function with given fields:\n", fname)
	} else {
		g.printf(
			"// %s provides a mock function with given fields: %s\n", fname,
			strings.Join(params.Names, ", "),
		)
	}
	g.printf(
		"func (_m *%s) %s(%s) ",
		g.mockName(),
		fname,
		strings.Join(params.Params, ", "),
	)

	switch len(returns.Types) {
	case 0:
		g.printf("{\n")
	case 1:
		g.printf("%s {\n", returns.Types[0])
	default:
		g.printf("(%s) {\n", strings.Join(returns.Types, ", "))
	}

	formattedParamNames := g.formattedParamNames(params)
	called := g.generateCalled(params, formattedParamNames) // _m.Called invocation string

	if len(returns.Types) > 0 {
		g.printf("\tret := %s\n\n", called)

		var ret []string

		for idx, typ := range returns.Types {
			g.printf("\tvar r%d %s\n", idx, typ)
			g.printf("\tif rf, ok := ret.Get(%d).(func(%s) %s); ok {\n",
				idx, strings.Join(params.Types, ", "), typ)
			g.printf("\t\tr%d = rf(%s)\n", idx, formattedParamNames)
			g.printf("\t} else {\n")
			if typ == "error" {
				g.printf("\t\tr%d = ret.Error(%d)\n", idx, idx)
			} else if returns.Nilable[idx] {
				g.printf("\t\tif ret.Get(%d) != nil {\n", idx)
				g.printf("\t\t\tr%d = ret.Get(%d).(%s)\n", idx, idx, typ)
				g.printf("\t\t}\n")
			} else {
				g.printf("\t\tr%d = ret.Get(%d).(%s)\n", idx, idx, typ)
			}
			g.printf("\t}\n\n")

			ret = append(ret, fmt.Sprintf("r%d", idx))
		}

		g.printf("\treturn %s\n", strings.Join(ret, ", "))
	} else {
		g.printf("\t%s\n", called)
	}

	g.printf("}\n\n")
}

func (g *Generator) mockMethodExpectation(expectationName, fname string, params, returns *paramList) {
	methodExpectationName := g.mockName() + fname + "Expectation"
	g.printf("type %s struct {\n\tcall *mock.Call\n}\n\n", methodExpectationName)

	g.printf("func (_e *%s) %s(%s) *%s {\n\t",
		expectationName,
		fname,
		strings.Join(params.Params, ", "),
		methodExpectationName,
	)

	call := g.generateCall(fname, params, g.formattedParamNames(params)) // _m.Mock.On invocation string
	g.printf(
		"return &%s{\n\t\tcall: %s,\n\t}\n",
		methodExpectationName,
		call,
	)
	g.printf("}\n\n")
	g.printf("func (_e *%s) ToReturn(%s) *mock.Call {\n\treturn _e.call.Return(%s)\n}\n",
		methodExpectationName,
		g.formattedReturnNames(returns),
		strings.Join(returns.Names, ","),
	)
}

func (g *Generator) formattedParamNames(params *paramList) string {
	var formattedParamNames string
	for i, name := range params.Names {
		if i > 0 {
			formattedParamNames += ", "
		}

		paramType := params.Types[i]
		// for variable args, move the ... to the end.
		if strings.Index(paramType, "...") == 0 {
			name += "..."
		}
		formattedParamNames += name
	}
	return formattedParamNames
}

func (g *Generator) formattedReturnNames(returns *paramList) string {
	var formattedReturnNames string
	for i, name := range returns.Names {
		if i > 0 {
			formattedReturnNames += ", "
		}

		returnType := returns.Types[i]
		formattedReturnNames += name + " " + returnType
	}
	return formattedReturnNames
}

// generateCalled returns the Mock.Called invocation string and, if necessary, prints the
// steps to prepare its argument list.
//
// It is separate from Generate to avoid cyclomatic complexity through early return statements.
func (g *Generator) generateCalled(list *paramList, formattedParamNames string) string {
	namesLen := len(list.Names)
	if namesLen == 0 {
		return "_m.Called()"
	}

	if !list.Variadic {
		return "_m.Called(" + formattedParamNames + ")"
	}

	variadicArgsName := g.varargs(list)

	// _ca will hold all arguments we'll mirror into Called, one argument per distinct value
	// passed to the method.
	//
	// For example, if the second argument is variadic and consists of three values,
	// a total of 4 arguments will be passed to Called. The alternative is to
	// pass a total of 2 arguments where the second is a slice with those 3 values from
	// the variadic argument. But the alternative is less accessible because it requires
	// building a []interface{} before calling Mock methods like On and AssertCalled for
	// the variadic argument, and creates incompatibility issues with the diff algorithm
	// in github.com/stretchr/testify/mock.
	//
	// This mirroring will allow argument lists for methods like On and AssertCalled to
	// always resemble the expected calls they describe and retain compatibility.
	//
	// It's okay for us to use the interface{} type, regardless of the actual types, because
	// Called receives only interface{} anyway.
	g.printf("\tvar _ca []interface{}\n")

	if namesLen > 1 {
		nonVariadicParamNames := formattedParamNames[0:strings.LastIndex(formattedParamNames, ",")]
		g.printf("\t_ca = append(_ca, %s)\n", nonVariadicParamNames)
	}
	g.printf("\t_ca = append(_ca, %s...)\n", variadicArgsName)

	return "_m.Called(_ca...)"
}

// generateCall returns the Mock.On invocation string and, if necessary, prints the
// steps to prepare its argument list.
//
// It is separate from Generate to avoid cyclomatic complexity through early return statements.
func (g *Generator) generateCall(fname string, list *paramList, formattedParamNames string) string {
	namesLen := len(list.Names)
	if namesLen == 0 {
		return fmt.Sprintf("_e.mock.On(\"%s\")", fname)
	}

	if !list.Variadic {
		return fmt.Sprintf("_e.mock.On(\"%s\", %s)", fname, formattedParamNames)
	}

	variadicArgsName := g.varargs(list)

	// _ca will hold all arguments we'll mirror into On, one argument per distinct value
	// passed to the method.
	//
	// For example, if the second argument is variadic and consists of three values,
	// a total of 4 arguments will be passed to Called. The alternative is to
	// pass a total of 2 arguments where the second is a slice with those 3 values from
	// the variadic argument. But the alternative is less accessible because it requires
	// building a []interface{} before calling Mock methods like On and AssertCalled for
	// the variadic argument, and creates incompatibility issues with the diff algorithm
	// in github.com/stretchr/testify/mock.
	//
	// It's okay for us to use the interface{} type, regardless of the actual types, because
	// On receives only interface{} anyway.
	g.printf("\tvar _ca []interface{}\n")

	if namesLen > 1 {
		nonVariadicParamNames := formattedParamNames[0:strings.LastIndex(formattedParamNames, ",")]
		g.printf("\t_ca = append(_ca, %s)\n", nonVariadicParamNames)
	}
	g.printf("\t_ca = append(_ca, %s...)\n", variadicArgsName)

	return fmt.Sprintf("_e.mock.On(\"%s\", _ca...)", fname)
}

// varargs generates varargs as a list of []interface{}
func (g *Generator) varargs(list *paramList) string {
	namesLen := len(list.Names)
	var variadicArgsName string
	variadicName := list.Names[namesLen-1]
	variadicIface := strings.Contains(list.Types[namesLen-1], "interface{}")

	if variadicIface {
		// Variadic is already of the interface{} type, so we don't need special handling.
		variadicArgsName = variadicName
	} else {
		// Define _va to avoid "cannot use t (type T) as type []interface {} in append" error
		// whenever the variadic type is non-interface{}.
		g.printf("\t_va := make([]interface{}, len(%s))\n", variadicName)
		g.printf("\tfor _i := range %s {\n\t\t_va[_i] = %s[_i]\n\t}\n", variadicName, variadicName)
		variadicArgsName = "_va"
	}
	return variadicArgsName
}

func (g *Generator) Write(w io.Writer) error {
	opt := &imports.Options{Comments: true}
	theBytes := g.buf.Bytes()

	res, err := imports.Process("mock.go", theBytes, opt)
	if err != nil {
		line := "--------------------------------------------------------------------------------------------"
		fmt.Fprintf(os.Stderr, "Between the lines is the file (mock.go) mockery generated in-memory but detected as invalid:\n%s\n%s\n%s\n", line, g.buf.String(), line)
		return err
	}

	_, err = w.Write(res)
	return err
}
