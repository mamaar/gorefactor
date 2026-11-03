package tests_test

import (
	"context"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

const stdMarshalJSON = "package p\n\ntype T struct{}\n\nfunc (T) MarshalJSON() ([]byte, error) { return []byte(\"{}\"), nil }\n"

// stdAddParamArgs adds an int parameter n after the existing parameter name of type typ.
func stdAddParamArgs(file, fn, name, typ string) map[string]any {
	params := []map[string]any{{"name": name, "type": typ}, {"name": "n", "type": "int"}}
	return map[string]any{"function_name": fn, "source_file": file, "subcommand": "add_param", "params": params, "default_value": "0", "position": 1}
}

func TestStdMethodSignatures_RemoveReturnRefusesMarshalJSONAndLeavesFilesAlone(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{"p/j.go": stdMarshalJSON})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := changeSig(t, sess, returnArgs("T.MarshalJSON", "p/j.go", "remove_return", "[]byte"))

	assertRefused(t, res, "T.MarshalJSON", "json.Marshaler")
	assertFileUnchanged(t, dir, "p/j.go", stdMarshalJSON)
}

func TestStdMethodSignatures_RenameMethodRefusesString(t *testing.T) {
	const src = "package p\n\ntype T struct{}\n\nfunc (T) String() string { return \"t\" }\n"
	dir := writeInlineWorkspace(t, map[string]string{"p/t.go": src})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := renameMethod(t, sess, map[string]any{"type_name": "T", "method_name": "String", "new_method_name": "Name"})

	assertRefused(t, res, "T.String", "fmt.Stringer")
	assertFileUnchanged(t, dir, "p/t.go", src)
}

func TestStdMethodSignatures_ChangeSignatureRefusesStdMethodButAllowsSameNameWithOtherSignature(t *testing.T) {
	const std = "package p\n\ntype T struct{}\n\nfunc (T) String() string { return \"t\" }\n"
	const other = "package p\n\ntype U struct{}\n\nfunc (U) String(x int) string { return \"u\" }\n"
	dir := writeInlineWorkspace(t, map[string]string{"p/t.go": std, "p/u.go": other})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	assertRefused(t, changeSig(t, sess, stdAddParamArgs("p/t.go", "T.String", "x", "int")), "T.String", "fmt.Stringer")
	assertFileUnchanged(t, dir, "p/t.go", std)

	changeSigOK(t, sess, stdAddParamArgs("p/u.go", "U.String", "x", "int"))
	assertFileContains(t, dir, "p/u.go", "func (U) String(x int, n int) string")
	goVets(t, dir, "")
}

func TestStdMethodSignatures_IsOnNonErrorTypeIsNotStd(t *testing.T) {
	const src = "package p\n\ntype T struct{}\n\nfunc (T) Is(e error) bool { return false }\n"
	dir := writeInlineWorkspace(t, map[string]string{"p/t.go": src})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	changeSigOK(t, sess, stdAddParamArgs("p/t.go", "T.Is", "e", "error"))
	assertFileContains(t, dir, "p/t.go", "func (T) Is(e error, n int) bool")
	goVets(t, dir, "")
}

func TestStdMethodSignatures_AddContextParameterRefusesError(t *testing.T) {
	const src = "package p\n\ntype E struct{}\n\nfunc (E) Error() string { return \"e\" }\n"
	for _, thread := range []bool{false, true} {
		dir := writeInlineWorkspace(t, map[string]string{
			"p/e.go":   src,
			"p/use.go": "package p\n\nfunc Use() string { return E{}.Error() }\n",
		})
		sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)

		args := map[string]any{"function_name": "E.Error", "source_file": "p/e.go", "thread": thread}
		res := addContext(t, sess, args)
		sess.Close()

		assertRefused(t, res, "E.Error", "it implements error")
		assertFileUnchanged(t, dir, "p/e.go", src)
	}
}

func TestStdMethodSignatures_AlwaysNilReportsMarshalJSONNotFixable(t *testing.T) {
	out := callAlwaysNilErrors(t, map[string]string{"p/j.go": stdMarshalJSON})

	wantNilRefused(t, findAlwaysNil(t, out, "T.MarshalJSON"), "json.Marshaler")
}

const stdUnmarshal = "package p\n\ntype T struct{}\n\nfunc (T) UnmarshalJSON(data []byte) error { return nil }\n"

func TestStdMethodSignatures_UnusedParamsReportsUnmarshalJSONNotFixable(t *testing.T) {
	out := callUnusedParams(t, nil, map[string]string{"p/j.go": stdUnmarshal})

	p := findParam(t, out, "T.UnmarshalJSON", "data")
	if p.Fixable || !strings.Contains(p.Reason, "json.Unmarshaler") {
		t.Errorf("fixable=%v reason=%q, want a refusal naming json.Unmarshaler", p.Fixable, p.Reason)
	}
}

func TestStdMethodSignatures_MessageNamesTheInterfaceWithoutVetForAdditions(t *testing.T) {
	const src = "package p\n\ntype T struct{}\n\nfunc (T) MarshalText() ([]byte, error) { return nil, nil }\n"
	dir := writeInlineWorkspace(t, map[string]string{"p/t.go": src})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := changeSig(t, sess, returnArgs("T.MarshalText", "p/t.go", "remove_return", "[]byte"))

	assertRefused(t, res, "encoding.TextMarshaler")

	if strings.Contains(errText(res), "go vet") {
		t.Errorf("refusal mentions go vet for an addition: %s", errText(res))
	}
}

func TestStdMethodSignatures_BareNameRemoveReturnRefusesMarshalJSON(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{"p/j.go": stdMarshalJSON})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := changeSig(t, sess, returnArgs("MarshalJSON", "p/j.go", "remove_return", "[]byte"))

	assertRefused(t, res, "json.Marshaler")
	assertFileUnchanged(t, dir, "p/j.go", stdMarshalJSON)
}

func TestStdMethodSignatures_BareNameAddContextParameterRefusesError(t *testing.T) {
	const src = "package p\n\ntype E struct{}\n\nfunc (E) Error() string { return \"e\" }\n"
	dir := writeInlineWorkspace(t, map[string]string{"p/e.go": src})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := addContext(t, sess, map[string]any{"function_name": "Error", "source_file": "p/e.go"})

	assertRefused(t, res, "it implements error")
	assertFileUnchanged(t, dir, "p/e.go", src)
}

func TestStdMethodSignatures_BareNameAddParamRefusesWriteSatisfyingIoWriter(t *testing.T) {
	const src = "package p\n\nimport \"io\"\n\ntype W struct{}\n\nfunc (W) Write(p []byte) (int, error) { return len(p), nil }\n\nfunc Use(r io.Reader) { _ = r }\n"
	dir := writeInlineWorkspace(t, map[string]string{"p/w.go": src})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := changeSig(t, sess, stdAddParamArgs("p/w.go", "Write", "p", "[]byte"))

	assertRefused(t, res, "io.Writer")
	assertFileUnchanged(t, dir, "p/w.go", src)
}

func TestStdMethodSignatures_RenameSymbolRefusesStdMethodDotted(t *testing.T) {
	const src = "package p\n\ntype T struct{}\n\nfunc (T) String() string { return \"t\" }\n"
	dir := writeInlineWorkspace(t, map[string]string{"p/t.go": src})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := callTool(t, sess, "rename_symbol", map[string]any{"symbol": "T.String", "new_name": "Name"})

	assertRefused(t, res, "fmt.Stringer")
	assertFileUnchanged(t, dir, "p/t.go", src)
}

func TestStdMethodSignatures_RenameSymbolRenamesPlainMethod(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"p/t.go":   "package p\n\ntype T struct{}\n\nfunc (T) Helper() {}\n",
		"p/use.go": "package p\n\nfunc Use(t T) { t.Helper() }\n",
	})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	res := callTool(t, sess, "rename_symbol", map[string]any{"symbol": "T.Helper", "new_name": "Assist"})

	if res.IsError {
		t.Fatalf("rename_symbol refused: %s", errText(res))
	}

	assertFileContains(t, dir, "p/t.go", "func (T) Assist()")
	assertFileContains(t, dir, "p/use.go", "t.Assist()")
	goVets(t, dir, "")
}

func TestStdMethodSignatures_RenameMethodRefusesNewNameThatIsStdMethod(t *testing.T) {
	const name = "package p\n\ntype T struct{}\n\nfunc (T) Name() string { return \"t\" }\n"

	const encode = "package p\n\ntype T struct{}\n\nfunc (T) Encode() ([]byte, error) { return nil, nil }\n"
	for _, c := range []struct{ src, method, newName, iface string }{
		{name, "Name", "String", "fmt.Stringer"},
		{encode, "Encode", "MarshalJSON", "json.Marshaler"},
	} {
		dir := writeInlineWorkspace(t, map[string]string{"p/t.go": c.src})
		sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)

		res := renameMethod(t, sess, map[string]any{"type_name": "T", "method_name": c.method, "new_method_name": c.newName})
		assertRefused(t, res, c.iface)
		assertFileUnchanged(t, dir, "p/t.go", c.src)

		res = callTool(t, sess, "rename_symbol", map[string]any{"symbol": "T." + c.method, "new_name": c.newName})
		assertRefused(t, res, c.iface)
		assertFileUnchanged(t, dir, "p/t.go", c.src)
		sess.Close()
	}
}

func TestStdMethodSignatures_RenameMethodAllowsNewNameWhenSignatureDiffers(t *testing.T) {
	const src = "package p\n\ntype T struct{}\n\nfunc (T) Name(x int) string { return \"t\" }\n"
	dir := writeInlineWorkspace(t, map[string]string{
		"p/t.go":   src,
		"p/use.go": "package p\n\nfunc Use(t T) string { return t.Name(1) }\n",
	})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	callJSON(t, sess, "rename_method", map[string]any{"type_name": "T", "method_name": "Name", "new_method_name": "String"})

	assertFileContains(t, dir, "p/t.go", "func (T) String(x int) string")
	assertFileContains(t, dir, "p/use.go", "t.String(1)")
	goVets(t, dir, "")
}

func TestStdMethodSignatures_CallSitesOfSkipsStdMethodWithReason(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{
		"lg/lg.go": logPkg,
		"main.go":  "package main\n\nimport \"example.com/threadmcp/p\"\n\nfunc main() { p.Run() }\n",
		"p/p.go":   "package p\n\nimport \"example.com/threadmcp/lg\"\n\ntype S struct{}\n\nfunc (S) String() string { lg.Warn(\"x\"); return \"s\" }\n\nfunc Run() { _ = S{}.String() }\n",
	})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	out := callJSON(t, sess, "add_context_parameter", callSitesArgs(false))

	if !hasReason(out, "standard method:") {
		t.Errorf("skipped_sites reasons = %v, want one starting standard method:", skippedReasons(out))
	}

	assertFileUnchanged(t, dir, "p/p.go", "package p\n\nimport \"example.com/threadmcp/lg\"\n\ntype S struct{}\n\nfunc (S) String() string { lg.Warn(\"x\"); return \"s\" }\n\nfunc Run() { _ = S{}.String() }\n")
}
