package dto

import (
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
)

// Назначение выноски (0388): spec — ЧЕТВЁРТЫЙ хвост подписи DESIGN. Как и у наконечника, опасность
// вся в том, чего фича НЕ делает: карточка без spec обязана хешироваться байт в байт как до 0388.

// specDigestFixture — мерка со стрелкой и пунктиром плюс пин: открыты все три прежних хвоста, так
// что ошибка в условии четвёртого сдвинула бы именно уже подписанные кортежи.
func specDigestFixture() *entity.TechCardInsert {
	tc := capsDigestFixture()
	tc.Callouts[0].Caps = entity.AnnotationCapsArrow
	tc.Callouts[0].Dashed = true
	tc.Callouts = append(tc.Callouts, entity.TechCardCallout{
		Number: 3, Part: ns("подборт"), MediaId: nullInt32FromPb(11),
		PosX: calloutPos("0.1"), PosY: calloutPos("0.1"),
	})
	return tc
}

// ЧИСЛО СНЯТО С БАЗОВОГО ДЕРЕВА (код ДО 0388, через `go test -overlay` с прежним
// techcard_section_digest.go), а не с этого.
const pre0388SpecFixtureDesignDigest = "cb30c43aeba9ad1d9b64bc8b619149fcad1c16082948deb826e0169de6289d62"

func TestSpecDigestUnchangedWhenSpecEmpty(t *testing.T) {
	require.Equal(t, pre0362DrawnCalloutDesignDigest,
		TechCardSectionDigests(capsDigestFixture())[entity.SignoffDesign])
	require.Equal(t, pre0388SpecFixtureDesignDigest,
		TechCardSectionDigests(specDigestFixture())[entity.SignoffDesign],
		"карточка без spec обязана хешироваться как до 0388")
}

func TestSpecDigestMovesOnNonEmptySpec(t *testing.T) {
	base := TechCardSectionDigests(specDigestFixture())[entity.SignoffDesign]
	seen := map[string]int{base: -1}
	for i := range specDigestFixture().Callouts {
		tc := specDigestFixture()
		tc.Callouts[i].Spec = ns(`{"t":"note"}`)
		d := TechCardSectionDigests(tc)[entity.SignoffDesign]
		if prev, ok := seen[d]; ok {
			t.Fatalf("spec на выноске %d дал тот же отпечаток, что %d", i, prev)
		}
		seen[d] = i
	}
	a, b := specDigestFixture(), specDigestFixture()
	a.Callouts[2].Spec = ns(`{"t":"note"}`)
	b.Callouts[2].Spec = ns(`{"t":"stitch"}`)
	require.NotEqual(t,
		TechCardSectionDigests(a)[entity.SignoffDesign],
		TechCardSectionDigests(b)[entity.SignoffDesign])
}

func TestSpecCarriedWithOmittedKind(t *testing.T) {
	stored := &entity.TechCard{TechCardInsert: *specDigestFixture()}
	stored.Callouts[0].Spec = ns(`{"t":"detail"}`)
	in := specDigestFixture()
	in.Callouts[0].Caps = ""
	in.Callouts[0].KindOmitted = true
	CarryOmittedCalloutGeometry(stored, in)
	require.Equal(t, `{"t":"detail"}`, in.Callouts[0].Spec.String)
	require.Equal(t, entity.AnnotationCapsArrow, in.Callouts[0].Caps)

}

// Бандл до 0388 шлёт вид, но spec — пустой строкой: хранимое назначение обязано пережить сохранение.
func TestSpecCarriedWhenKindPresentAndSpecEmpty(t *testing.T) {
	stored := &entity.TechCard{TechCardInsert: *specDigestFixture()}
	stored.Callouts[0].Spec = ns(`{"t":"detail"}`)
	_, omitted, err := calloutSpecFromPb("callouts[0]", "")
	require.NoError(t, err)
	require.True(t, omitted)
	in := specDigestFixture()
	in.Callouts[0].SpecOmitted = omitted
	CarryOmittedCalloutGeometry(stored, in)
	require.Equal(t, `{"t":"detail"}`, in.Callouts[0].Spec.String)
}

// "{}" — явная очистка: хранится NULL, не переносится, и хешируется как карточка без spec.
func TestSpecEmptyObjectClears(t *testing.T) {
	spec, omitted, err := calloutSpecFromPb("callouts[0]", ` { } `)
	require.NoError(t, err)
	require.False(t, omitted)
	require.Equal(t, "", spec)

	stored := &entity.TechCard{TechCardInsert: *specDigestFixture()}
	stored.Callouts[0].Spec = ns(`{"t":"detail"}`)
	in := specDigestFixture()
	in.Callouts[0].Spec = nullStringFromPb(spec)
	CarryOmittedCalloutGeometry(stored, in)
	require.False(t, in.Callouts[0].Spec.Valid)
	require.Equal(t, pre0388SpecFixtureDesignDigest, TechCardSectionDigests(in)[entity.SignoffDesign])
}

func TestSpecCanonicalizedOnWrite(t *testing.T) {
	a, _, err := calloutSpecFromPb("callouts[0]", `{"b":1,"a":2}`)
	require.NoError(t, err)
	b, _, err := calloutSpecFromPb("callouts[0]", ` { "a" : 2 , "b" : 1 } `)
	require.NoError(t, err)
	require.Equal(t, a, b)
	require.Equal(t, `{"a":2,"b":1}`, a)

	// И то, что MySQL отдаёт на чтении (свои пробелы, свой порядок), приводится к тому же.
	r, err := entity.CanonicalCalloutSpec(`{"b": 1, "a": 2}`)
	require.NoError(t, err)
	require.Equal(t, a, r)

	empty, omitted, err := calloutSpecFromPb("callouts[0]", "")
	require.NoError(t, err)
	require.Equal(t, "", empty)
	require.True(t, omitted)
}

func TestSpecRefusesNonObject(t *testing.T) {
	for _, bad := range []string{`[1,2]`, `"note"`, `5`, `null`, `{"t":`, strings.Repeat(" ", entity.MaxCalloutSpecBytes) + `{}`} {
		_, _, err := calloutSpecFromPb("callouts[0]", bad)
		require.Error(t, err, "spec %q must be refused", bad)
	}
	_, _, err := calloutSpecFromPb("callouts[0]", `{"t":"`+strings.Repeat("x", entity.MaxCalloutSpecBytes)+`"}`)
	require.Error(t, err)
}

func TestSpecRoundTripsThroughTheWire(t *testing.T) {
	pb := &pb_common.TechCardCallout{Number: 1, Spec: `{"t":"note","b":1}`}
	spec, _, err := calloutSpecFromPb("callouts[0]", pb.Spec)
	require.NoError(t, err)
	require.Equal(t, `{"b":1,"t":"note"}`, spec)
}
