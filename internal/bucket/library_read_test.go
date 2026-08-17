package bucket

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// TestGetLibraryObjectRefusesForeignKeys locks the guard that keeps the FIRST object read in this
// package from becoming an oracle over the whole bucket. The Bucket here has NO client at all: a key
// that gets past the guard would nil-panic, so the test proves the refusal happens before anything
// is fetched, not merely that an error comes back.
func TestGetLibraryObjectRefusesForeignKeys(t *testing.T) {
	b := &Bucket{}
	ctx := context.Background()

	foreign := map[string]string{
		"pattern key":     "base/tech-card-patterns/2026/august/sheet.pdf",
		"media key":       "base/media/2026/august/x.jpg",
		"label key":       "base/shipping-labels/2026/x.pdf",
		"folder is last":  "base/files-library",
		"trailing slash":  "base/files-library/",
		"parent segment":  "base/files-library/../../secrets/x.pdf",
		"substring only":  "base/files-library-public/x.md",
		"empty":           "",
		"root of nothing": "/",
	}
	for name, key := range foreign {
		data, err := b.GetLibraryObject(ctx, key)
		if err == nil {
			t.Errorf("%s: GetLibraryObject(%q) must be refused", name, key)
			continue
		}
		if !errors.Is(err, ErrLibraryObjectKeyNotManaged) {
			t.Errorf("%s: GetLibraryObject(%q) = %v, want ErrLibraryObjectKeyNotManaged", name, key, err)
		}
		if data != nil {
			t.Errorf("%s: a refused read must return no bytes, got %d", name, len(data))
		}
	}
}

// TestReadWithinLimitRefusesRatherThanTruncates is the other half of the guard. Усечение здесь было
// бы худшим исходом из возможных: заметка, прочитанная наполовину, выглядит как заметка, которую
// человек сам укоротил, — и первое же сохранение записало бы эту половину поверх целого текста.
func TestReadWithinLimitRefusesRatherThanTruncates(t *testing.T) {
	const limit = 16

	exact := strings.Repeat("a", limit)
	got, err := readWithinLimit(strings.NewReader(exact), limit)
	if err != nil {
		t.Fatalf("a body of exactly the limit must be read: %v", err)
	}
	if string(got) != exact {
		t.Fatalf("read %q, want %q", got, exact)
	}

	over := strings.Repeat("a", limit+1)
	got, err = readWithinLimit(strings.NewReader(over), limit)
	if !errors.Is(err, ErrLibraryObjectTooLarge) {
		t.Fatalf("a body one byte over the limit = %v, want ErrLibraryObjectTooLarge", err)
	}
	if got != nil {
		t.Fatalf("a refused read must return no bytes, got %d", len(got))
	}

	// Пустой объект — законная заметка («создал, чтобы завтра написать»), а не ошибка.
	got, err = readWithinLimit(strings.NewReader(""), limit)
	if err != nil || len(got) != 0 {
		t.Fatalf("an empty object must read as empty: %v, %d bytes", err, len(got))
	}
}

// TestLibraryReadLimitCoversTheNoteCap guards the relationship the two constants must keep: a note
// written right up to its own cap has to be READABLE afterwards. Equal limits would make the largest
// legal note unopenable — the one failure nobody would think to test by hand.
func TestLibraryReadLimitCoversTheNoteCap(t *testing.T) {
	if maxLibraryReadBytes <= entity.MaxLibraryNoteBytes {
		t.Fatalf("read limit %d must exceed the note cap %d", maxLibraryReadBytes, entity.MaxLibraryNoteBytes)
	}
}
