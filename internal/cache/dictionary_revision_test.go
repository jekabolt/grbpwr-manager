package cache

import (
	"context"
	"errors"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

type fakeRevSrc struct {
	revs  map[entity.DictionaryNamespace]int64
	calls int
	err   error
}

func (f *fakeRevSrc) GetDictionaryRevisions(context.Context) (map[entity.DictionaryNamespace]int64, error) {
	f.calls++
	return f.revs, f.err
}

type fakeInfoSrc struct {
	calls int
	err   error
}

func (f *fakeInfoSrc) GetDictionaryInfo(context.Context) (*entity.DictionaryInfo, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return &entity.DictionaryInfo{}, nil
}

func resetRevisions() { SetDictionaryRevisions(map[entity.DictionaryNamespace]int64{}) }

func TestDictionaryRevisionsStale(t *testing.T) {
	resetRevisions()
	SetDictionaryRevisions(map[entity.DictionaryNamespace]int64{entity.DictNamespaceColor: 2})

	if DictionaryRevisionsStale(map[entity.DictionaryNamespace]int64{entity.DictNamespaceColor: 2}) {
		t.Error("equal revision should not be stale")
	}
	if !DictionaryRevisionsStale(map[entity.DictionaryNamespace]int64{entity.DictNamespaceColor: 3}) {
		t.Error("higher DB revision should be stale")
	}
	if !DictionaryRevisionsStale(map[entity.DictionaryNamespace]int64{entity.DictNamespaceCollection: 1}) {
		t.Error("never-seen namespace should be stale")
	}
}

func TestEnsureDictionaryFresh(t *testing.T) {
	ctx := context.Background()
	resetRevisions()

	rev := &fakeRevSrc{revs: map[entity.DictionaryNamespace]int64{
		entity.DictNamespaceColor:      1,
		entity.DictNamespaceCollection: 1,
	}}
	info := &fakeInfoSrc{}

	reloaded, err := EnsureDictionaryFresh(ctx, rev, info)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if !reloaded || info.calls != 1 {
		t.Fatalf("first call should reload once, reloaded=%v infoCalls=%d", reloaded, info.calls)
	}

	reloaded, err = EnsureDictionaryFresh(ctx, rev, info)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if reloaded || info.calls != 1 {
		t.Fatalf("unchanged revisions must not reload, reloaded=%v infoCalls=%d", reloaded, info.calls)
	}

	rev.revs[entity.DictNamespaceColor] = 2
	reloaded, err = EnsureDictionaryFresh(ctx, rev, info)
	if err != nil {
		t.Fatalf("third call: %v", err)
	}
	if !reloaded || info.calls != 2 {
		t.Fatalf("bumped revision must reload again, reloaded=%v infoCalls=%d", reloaded, info.calls)
	}
}

func TestEnsureDictionaryFreshErrors(t *testing.T) {
	ctx := context.Background()
	resetRevisions()

	revErr := &fakeRevSrc{err: errors.New("db down")}
	info := &fakeInfoSrc{}
	if _, err := EnsureDictionaryFresh(ctx, revErr, info); err == nil {
		t.Error("revision read error must propagate")
	}
	if info.calls != 0 {
		t.Error("must not reload when revisions cannot be read")
	}

	rev := &fakeRevSrc{revs: map[entity.DictionaryNamespace]int64{entity.DictNamespaceTag: 5}}
	infoErr := &fakeInfoSrc{err: errors.New("reload failed")}
	if _, err := EnsureDictionaryFresh(ctx, rev, infoErr); err == nil {
		t.Error("reload error must propagate")
	}
	// Cache revisions must remain unset so a later successful call still reloads.
	if !DictionaryRevisionsStale(map[entity.DictionaryNamespace]int64{entity.DictNamespaceTag: 5}) {
		t.Error("failed reload must not advance cached revisions")
	}
}
