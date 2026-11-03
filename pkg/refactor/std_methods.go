package refactor

import (
	"fmt"
	gotypes "go/types"
	"slices"
	"strings"

	pkgtypes "github.com/mamaar/gorefactor/pkg/types"
)

// stdMethod is one signature whose change is refused.
type stdMethod struct {
	signature string // parameters and results, spelled as go/types prints them
	iface     string // the interface the method satisfies, as diagnostics name it
	vet       bool   // false for the entries this package adds to vet's list
}

// stdMethods are the method signatures, keyed by name, that callers and the
// interface they satisfy depend on. The first group is vet's stdmethods
// canonicalMethods (golang.org/x/tools v0.52.0, where the table is unexported),
// with byte spelled uint8. The second group is added: Error and String, and the
// encoding interfaces that packages look up through reflection.
var stdMethods = map[string][]stdMethod{
	// vet's stdmethods
	"As":            {{"(any) bool", "error", true}},
	"Format":        {{"(fmt.State, int32)", "fmt.Formatter", true}},
	"GobDecode":     {{"([]uint8) error", "gob.GobDecoder", true}},
	"GobEncode":     {{"() ([]uint8, error)", "gob.GobEncoder", true}},
	"Is":            {{"(error) bool", "error", true}},
	"MarshalJSON":   {{"() ([]uint8, error)", "json.Marshaler", true}},
	"MarshalXML":    {{"(*encoding/xml.Encoder, encoding/xml.StartElement) error", "xml.Marshaler", true}},
	"ReadByte":      {{"() (uint8, error)", "io.ByteReader", true}},
	"ReadFrom":      {{"(io.Reader) (int64, error)", "io.ReaderFrom", true}},
	"ReadRune":      {{"() (int32, int, error)", "io.RuneReader", true}},
	"Scan":          {{"(fmt.ScanState, int32) error", "fmt.Scanner", true}},
	"Seek":          {{"(int64, int) (int64, error)", "io.Seeker", true}},
	"UnmarshalJSON": {{"([]uint8) error", "json.Unmarshaler", true}},
	"UnmarshalXML":  {{"(*encoding/xml.Decoder, encoding/xml.StartElement) error", "xml.Unmarshaler", true}},
	"UnreadByte":    {{"() error", "io.ByteScanner", true}},
	"UnreadRune":    {{"() error", "io.RuneScanner", true}},
	"Unwrap":        {{"() error", "error", true}, {"() []error", "error", true}},
	"WriteByte":     {{"(uint8) error", "io.ByteWriter", true}},
	"WriteTo":       {{"(io.Writer) (int64, error)", "io.WriterTo", true}},

	// added
	"AppendBinary":    {{"([]uint8) ([]uint8, error)", "encoding.BinaryAppender", false}},
	"AppendText":      {{"([]uint8) ([]uint8, error)", "encoding.TextAppender", false}},
	"Error":           {{"() string", "error", false}},
	"MarshalBinary":   {{"() ([]uint8, error)", "encoding.BinaryMarshaler", false}},
	"MarshalText":     {{"() ([]uint8, error)", "encoding.TextMarshaler", false}},
	"String":          {{"() string", "fmt.Stringer", false}},
	"UnmarshalBinary": {{"([]uint8) error", "encoding.BinaryUnmarshaler", false}},
	"UnmarshalText":   {{"([]uint8) error", "encoding.TextUnmarshaler", false}},
}

// stdMethodOf returns the table entry that a method named name, with f's
// receiver and signature, matches, or nil. Is, As and Unwrap match only on types
// that implement error, and variadic methods never match.
func stdMethodOf(name string, f *gotypes.Func) *stdMethod {
	sig := f.Type().(*gotypes.Signature)
	if sig.Recv() == nil || sig.Variadic() {
		return nil
	}

	signature := stdSignatureOf(sig)

	for i := range stdMethods[name] {
		m := &stdMethods[name][i]
		if m.signature != signature {
			continue
		}

		if slices.Contains([]string{"As", "Is", "Unwrap"}, name) &&
			!typeImplements(sig.Recv().Type(), errorType.Underlying().(*gotypes.Interface)) {
			return nil
		}

		return m
	}

	return nil
}

// stdSignatureOf renders the parameters and results of sig as the table does.
func stdSignatureOf(sig *gotypes.Signature) string {
	return "(" + stdTupleOf(sig.Params()) + ")" + stdResultsOf(sig.Results())
}

func stdResultsOf(results *gotypes.Tuple) string {
	switch results.Len() {
	case 0:
		return ""
	case 1:
		return " " + stdTypeOf(results.At(0).Type())
	}

	return " (" + stdTupleOf(results) + ")"
}

func stdTupleOf(t *gotypes.Tuple) string {
	names := make([]string, t.Len())
	for i := range names {
		names[i] = stdTypeOf(t.At(i).Type())
	}

	return strings.Join(names, ", ")
}

// stdTypeOf spells t as the table does. A type the table has no spelling for
// keeps go/types' own spelling, which matches nothing in the table.
func stdTypeOf(t gotypes.Type) string {
	t = gotypes.Unalias(t)
	switch t := t.(type) {
	case *gotypes.Basic:
		return gotypes.Typ[t.Kind()].Name()
	case *gotypes.Named:
		obj := t.Obj()
		if obj.Pkg() == nil {
			return obj.Name()
		}

		return obj.Pkg().Path() + "." + obj.Name()
	case *gotypes.Slice:
		return "[]" + stdTypeOf(t.Elem())
	case *gotypes.Pointer:
		return "*" + stdTypeOf(t.Elem())
	case *gotypes.Interface:
		if t.Empty() {
			return "any"
		}
	}

	return gotypes.TypeString(t, nil)
}

// vetClause names go vet for the entries vet checks, and is empty for additions.
func vetClause(m *stdMethod) string {
	if m.vet {
		return ", whose method signature go vet checks"
	}

	return ""
}

// stdMethodWhy says what changing a method that matches m would break.
func stdMethodWhy(m *stdMethod) string {
	return fmt.Sprintf("it implements %s%s, so the change would stop satisfying %s", m.iface, vetClause(m), m.iface)
}

// stdMethodError refuses the verb, such as change or rename, on f when f is a
// standard method.
func stdMethodError(verb string, f *gotypes.Func) error {
	m := stdMethodOf(f.Name(), f)
	if m == nil {
		return nil
	}

	return &pkgtypes.RefactorError{
		Type:    pkgtypes.InvalidOperation,
		Message: fmt.Sprintf("cannot %s %s: %s. No changes were made.", verb, funcLabel(f), stdMethodWhy(m)),
	}
}

// stdRenameError refuses renaming f to newName when newName with f's signature
// is a standard method, which f would start to satisfy.
func stdRenameError(f *gotypes.Func, newName string) error {
	m := stdMethodOf(newName, f)
	if m == nil {
		return nil
	}

	return &pkgtypes.RefactorError{
		Type: pkgtypes.InvalidOperation,
		Message: fmt.Sprintf("cannot rename %s to %s: with this signature the new name would implement %s%s, so the method would start satisfying %s. No changes were made.",
			funcLabel(f), newName, m.iface, vetClause(m), m.iface),
	}
}
