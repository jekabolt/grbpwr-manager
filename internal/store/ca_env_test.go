package store

import "testing"

func TestDBCACertPEMPrefersAppPlatformSafeKey(t *testing.T) {
	t.Setenv("db.CA_CERT", "legacy")
	t.Setenv("MYSQL_TLS_CA_CERT", "")
	if got := dbCACertPEM(); got != "legacy" {
		t.Fatalf("legacy key: got %q", got)
	}
	t.Setenv("MYSQL_TLS_CA_CERT", "bound")
	if got := dbCACertPEM(); got != "bound" {
		t.Fatalf("MYSQL_TLS_CA_CERT must win: got %q", got)
	}
}

func TestNilStoreCloseIsSafe(t *testing.T) {
	var ms *MYSQLStore
	ms.Close()
}
