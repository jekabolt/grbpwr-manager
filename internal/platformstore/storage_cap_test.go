package platformstore

import "testing"

func TestStorageCapDefault(t *testing.T) {
	for in, want := range map[string]int64{
		"":      20 << 30,
		"20":    20 << 30,
		"1.5":   3 << 29,
		"0":     0,
		"off":   0,
		" OFF ": 0,
		"abc":   20 << 30, // a typo never turns the cap off
		"-3":    20 << 30,
	} {
		if got := (Config{StorageCapGB: in}).StorageCapDefault(); got != want {
			t.Errorf("%q = %d, want %d", in, got, want)
		}
	}
}
