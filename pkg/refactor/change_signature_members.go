package refactor

import (
	"fmt"
	gotypes "go/types"
	"strings"

	pkgtypes "github.com/mamaar/gorefactor/pkg/types"
)

// signatureMembers returns the methods that must change with the target method
// typeName.methodName, the target excluded: the workspace interface methods it
// satisfies and the implementations of those, found by go/types. It refuses a
// target that satisfies an interface outside the workspace, and, without
// propagate, a concrete target that satisfies a workspace interface.
func (op *ChangeSignatureOperation) signatureMembers(
	ws *pkgtypes.Workspace, pkg *pkgtypes.Package, typeName, methodName string, isIface bool,
) ([]*gotypes.Func, error) {
	seed, err := methodObject(ws, pkg, typeName, methodName)
	if err != nil {
		return nil, err
	}
	if err := stdMethodError("change", seed); err != nil {
		return nil, err
	}
	survey := surveyInterfaces(ws)
	if !isIface && !op.PropagateToInterface {
		if ext, ok := survey.externalInterfaceOf(seed); ok {
			return nil, externalInterfaceError("change", seed, ext)
		}
		if satisfied := survey.expansionOf(seed); len(satisfied) > 0 {
			return nil, &pkgtypes.RefactorError{
				Type: pkgtypes.InvalidOperation,
				Message: fmt.Sprintf("cannot change %s: it satisfies %s, which would stop being satisfied; "+
					"set propagate to change the interface and its implementations too. No changes were made.",
					funcLabel(seed), labelsOf(satisfied)),
			}
		}
		return nil, nil
	}

	members, err := propagatedMembers(survey, seed, func(f *gotypes.Func, ext ifaceCandidate) error {
		return externalInterfaceError("change", f, ext)
	})
	if err != nil {
		return nil, err
	}
	for _, m := range members {
		if err := stdMethodError("change", m); err != nil {
			return nil, err
		}
	}
	return members, nil
}

// propagatedMembers returns the methods that change with seed when the change
// propagates through interfaces, the seed excluded. refuse builds the error for
// a method that satisfies an interface outside the workspace.
func propagatedMembers(survey *ifaceSurvey, seed *gotypes.Func, refuse func(*gotypes.Func, ifaceCandidate) error) ([]*gotypes.Func, error) {
	members, err := methodClosure(survey, seed, refuse)
	if err != nil {
		return nil, err
	}
	return members[1:], nil // the closure starts with the seed
}

// methodObject returns the go/types method typeName.methodName of pkg.
func methodObject(ws *pkgtypes.Workspace, pkg *pkgtypes.Package, typeName, methodName string) (*gotypes.Func, error) {
	sym := symbolForKey(ws, pkgtypes.SymbolKey{PkgPath: pkg.ImportPath, Recv: typeName, Name: methodName})
	if sym == nil {
		return nil, &pkgtypes.RefactorError{
			Type:    pkgtypes.SymbolNotFound,
			Message: fmt.Sprintf("method %s.%s not found in package %s", typeName, methodName, pkg.ImportPath),
		}
	}
	return resolveMethod(pkg, typeName, methodName, sym.Position)
}

func labelsOf(funcs []*gotypes.Func) string {
	labels := make([]string, len(funcs))
	for i, f := range funcs {
		labels[i] = funcLabel(f)
	}
	return strings.Join(labels, ", ")
}

// applyMembers adds the signature change of each member to plan and returns
// the members' symbols, whose references need call-site updates.
func (op *ChangeSignatureOperation) applyMembers(
	ws *pkgtypes.Workspace, members []*gotypes.Func, plan *pkgtypes.RefactoringPlan,
) ([]*pkgtypes.Symbol, error) {
	if len(members) == 0 {
		return nil, nil
	}
	decls := buildFuncDeclIndex(ws)
	var symbols []*pkgtypes.Symbol
	for _, m := range members {
		sym, err := memberSymbol(ws, m)
		if err != nil {
			return nil, err
		}
		d := decls[canonicalFunc(m)]
		if d == nil {
			return nil, &pkgtypes.RefactorError{
				Type:    pkgtypes.SymbolNotFound,
				Message: fmt.Sprintf("cannot change %s: its declaration is not in a file of the workspace; no changes were made", funcLabel(m)),
			}
		}
		recv := receiverNamed(m).Obj().Name()
		change, err := op.memberChange(ws, d, recv, m.Name())
		if err != nil {
			return nil, err
		}
		plan.Changes = append(plan.Changes, change)
		op.recordAltered(d.Pkg, recv, m.Name(), d.File.Path)
		if !contains(plan.AffectedFiles, d.File.Path) {
			plan.AffectedFiles = append(plan.AffectedFiles, d.File.Path)
		}
		symbols = append(symbols, sym)
	}
	return symbols, nil
}

// memberChange returns the change rewriting the declaration d of recv.name.
func (op *ChangeSignatureOperation) memberChange(ws *pkgtypes.Workspace, d *funcDecl, recv, name string) (pkgtypes.Change, error) {
	if d.IfaceField != nil {
		return op.generateInterfaceMethodSignatureChange(ws, d.File, recv, name)
	}
	if op.RemoveParam {
		op.checkRemovedUnused(ws, d.Pkg, d.Decl)
	}
	return op.signatureChange(ws, d.File, d.Decl, fmt.Sprintf("Update implementation %s.%s", recv, name)), nil
}
