package tests_test

import (
	"context"
	"strings"
	"testing"

	"github.com/mamaar/gorefactor/tests/mcptest"
)

const anonIfaceF = "package p\n\ntype L struct{}\n\nfunc (L) Name() string { return \"\" }\n\nfunc (L) Styles() ([]int, error) { return nil, nil }\n"
const anonIfaceUse = "package p\n\nfunc use(l L) {\n\tx := interface {\n\t\tName() string\n\t\tStyles() ([]int, error)\n\t}(l)\n\t_, _ = x.Styles()\n}\n"

func TestChangeSignature_RefusesMethodSatisfyingAnonymousInterface(t *testing.T) {
	dir := writeInlineWorkspace(t, map[string]string{"p/f.go": anonIfaceF, "p/use.go": anonIfaceUse})

	sess := mcptest.Dial(context.Background(), t, mcpTransport(), dir)
	defer sess.Close()

	args := returnArgs("L.Styles", "p/f.go", "remove_return", "[]int")
	args["propagate"] = true
	res := changeSig(t, sess, args)

	if !res.IsError || !strings.Contains(errText(res), "anonymous interface") {
		t.Errorf("want a refusal naming the anonymous interface, got IsError=%v %q", res.IsError, errText(res))
	}

	assertFileUnchanged(t, dir, "p/f.go", anonIfaceF)
}

func TestAlwaysNilErrors_SatisfyingAnAnonymousInterfaceIsNotFixable(t *testing.T) {
	out := callAlwaysNilErrors(t, map[string]string{"p/f.go": anonIfaceF, "p/use.go": anonIfaceUse})

	wantNilRefused(t, findAlwaysNil(t, out, "L.Styles"), "anonymous interface")
}
