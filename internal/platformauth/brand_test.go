package platformauth

import (
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/status"
)

// brandRefusal is "Code key" of a gRPC error, "" for nil.
func brandRefusal(err error) string {
	if err == nil {
		return ""
	}
	return status.Code(err).String() + " " + status.Convert(err).Message()
}

func TestUnitNormalizeBrand(t *testing.T) {
	ok := func(req *pb_admin.SetTeamBrandRequest) platformstore.TeamBrand {
		t.Helper()
		b, err := NormalizeTeamBrand(req)
		require.NoError(t, err)
		return b
	}
	// Trim, collapse inner runs, case as typed, any script.
	b := ok(&pb_admin.SetTeamBrandRequest{
		BrandName:    "  Ateliér   Лекало  ",
		LegalEntity:  "ООО «Лекало»   Studio",
		LegalAddress: []string{"  ", "ул. Пушкина,   д. 1", "", "Москва  101000"},
		ContactEmail: "  Hello@Atelier.IO ",
		Website:      " HTTPS://Atelier.IO/ ",
	})
	require.Equal(t, platformstore.TeamBrand{
		BrandName:    "Ateliér Лекало",
		LegalEntity:  "ООО «Лекало» Studio",
		LegalAddress: []string{"ул. Пушкина, д. 1", "Москва 101000"},
		ContactEmail: "hello@atelier.io",
		Website:      "atelier.io",
	}, b)
	// Everything empty is a value: papers print nothing.
	require.Equal(t, platformstore.TeamBrand{}, ok(&pb_admin.SetTeamBrandRequest{LegalAddress: []string{"", " "}}))
	// Limits are runes, not bytes.
	require.Equal(t, strings.Repeat("ж", BrandNameMax), ok(&pb_admin.SetTeamBrandRequest{BrandName: strings.Repeat("ж", BrandNameMax)}).BrandName)
	require.Equal(t, strings.Repeat("ж", LegalEntityMax), ok(&pb_admin.SetTeamBrandRequest{LegalEntity: strings.Repeat("ж", LegalEntityMax)}).LegalEntity)
	require.Len(t, ok(&pb_admin.SetTeamBrandRequest{LegalAddress: []string{"a", "", "b", " ", "c"}}).LegalAddress, 3)
	for _, site := range []struct{ in, want string }{
		{"grbpwr.com", "grbpwr.com"},
		{"http://Shop.Example.com/", "shop.example.com"},
		{"https://example.com/brand", "example.com/brand"},
		{"https://example.com/brand/", "example.com/brand"},
		{"лекало.рф", "лекало.рф"},
	} {
		require.Equal(t, site.want, ok(&pb_admin.SetTeamBrandRequest{Website: site.in}).Website, site.in)
	}

	bad := map[string]*pb_admin.SetTeamBrandRequest{
		"name newline":        {BrandName: "a\nb"},
		"name bell":           {BrandName: "a\u0007b"},
		"name tab":            {BrandName: "a\tb"},
		"name line separator": {BrandName: "a b"},
		"name too long":       {BrandName: strings.Repeat("ж", BrandNameMax+1)},
		"entity too long":     {LegalEntity: strings.Repeat("a", LegalEntityMax+1)},
		"entity control":      {LegalEntity: "x\x00"},
		"address line long":   {LegalAddress: []string{strings.Repeat("a", AddressLineMax+1)}},
		"address newline":     {LegalAddress: []string{"a\r\nb"}},
		"name not utf8":       {BrandName: "\xff"},
	}
	for name, req := range bad {
		_, err := NormalizeTeamBrand(req)
		require.Equal(t, "InvalidArgument bad_text", brandRefusal(err), name)
	}
	_, err := NormalizeTeamBrand(&pb_admin.SetTeamBrandRequest{LegalAddress: []string{"a", "b", "", "c", "d"}})
	require.Equal(t, "InvalidArgument bad_address", brandRefusal(err))
	for _, email := range []string{"nope", "a@b", "Ann <a@b.io>", "a\n@b.io", "почта@b.io"} {
		_, err := NormalizeTeamBrand(&pb_admin.SetTeamBrandRequest{ContactEmail: email})
		require.Equal(t, "InvalidArgument bad_email", brandRefusal(err), email)
	}
	for _, site := range []string{"localhost", "exa mple.com", "ftp://example.com", ".example.com", "example.", "https://", "a.b/" + strings.Repeat("x", WebsiteMax)} {
		_, err := NormalizeTeamBrand(&pb_admin.SetTeamBrandRequest{Website: site})
		require.Equal(t, "InvalidArgument bad_website", brandRefusal(err), site)
	}
}
