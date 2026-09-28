package entity

import (
	"slices"
	"testing"
)

// The AI vocabularies are closed in Go and nowhere else (no ENUM, no CHECK in 0373/0374), so these
// probes ARE the database's constraint. Every test below names the mutation that turns it red.

// TestAIShapeProviderKeysAreTheNineAndClosed.
//
// MUTATION IT CATCHES: a key added to AIProviderKeys but not to IsAIProviderKey (or the reverse) — the
// panel would list a provider every write then refuses, or the store would accept one the panel never
// shows. Also: returning the package's own slice, which a caller could sort in place.
func TestAIShapeProviderKeysAreTheNineAndClosed(t *testing.T) {
	want := []string{"openai", "anthropic", "google", "openrouter", "apibost", "fal", "meshy", "runblob", "recraft"}
	got := AIProviderKeys()
	if !slices.Equal(got, want) {
		t.Fatalf("AIProviderKeys() = %v, want %v", got, want)
	}
	for _, k := range got {
		if !IsAIProviderKey(k) {
			t.Fatalf("%q is listed but IsAIProviderKey refuses it", k)
		}
		if len(AIProviderCapabilities(k)) == 0 {
			t.Fatalf("%q serves nothing: every provider must name its capabilities", k)
		}
	}
	for _, k := range []string{"", "OpenAI", "openai ", "orimages", "openrouter_images", "fal_cutout"} {
		if IsAIProviderKey(k) {
			t.Fatalf("IsAIProviderKey(%q) = true: the vocabulary must be closed and exact", k)
		}
	}
	got[0] = "mutated"
	if AIProviderKeys()[0] != AIProviderOpenAI {
		t.Fatal("AIProviderKeys must return a copy")
	}
}

// TestAIShapeProviderCapabilitiesMatchTheContract pins the table of 06-BRIEFS-A.
//
// MUTATION IT CATCHES: dropping `edit` from fal (image.extend / image.inpaint would then refuse the
// only provider that serves them today), giving anthropic `image`, or dropping `image` from runblob
// (B-31: the panel could no longer name it on image.generate).
func TestAIShapeProviderCapabilitiesMatchTheContract(t *testing.T) {
	want := map[string][]string{
		AIProviderOpenAI:     {AICapabilityChat, AICapabilityImage},
		AIProviderAnthropic:  {AICapabilityChat},
		AIProviderGoogle:     {AICapabilityChat, AICapabilityImage},
		AIProviderOpenRouter: {AICapabilityChat, AICapabilityImage},
		AIProviderApibost:    {AICapabilityChat, AICapabilityImage},
		AIProviderFal:        {AICapabilityImage, AICapabilityCutout, AICapabilityEdit, AICapabilityThreed},
		AIProviderMeshy:      {AICapabilityThreed},
		AIProviderRunblob:    {AICapabilityImage, AICapabilityVideo},
		AIProviderRecraft:    {AICapabilityVector},
	}
	for k, caps := range want {
		if got := AIProviderCapabilities(k); !slices.Equal(got, caps) {
			t.Fatalf("AIProviderCapabilities(%q) = %v, want %v", k, got, caps)
		}
		for _, c := range caps {
			if !IsAICapability(c) {
				t.Fatalf("%q serves %q, which is not a capability", k, c)
			}
			if !AIProviderServes(k, c) {
				t.Fatalf("AIProviderServes(%q, %q) = false", k, c)
			}
		}
	}
	if AIProviderCapabilities("nobody") != nil || AIProviderServes("nobody", AICapabilityChat) {
		t.Fatal("an unknown provider must serve nothing")
	}
	if AIProviderServes(AIProviderAnthropic, AICapabilityImage) {
		t.Fatal("anthropic does not serve images")
	}
}

// TestAIShapePurposesAreThePlansTwelve pins 02-PLAN §4.1 — less the retired operations draft (O-66,
// 0378) — and the purpose → capability map.
//
// MUTATION IT CATCHES: a purpose added to AIPurposes without a capability (IsAIPurpose is derived from
// the capability map, so it would read as unknown and every SetRoute would refuse it), or image.extend
// mapped to `image` (fal serves it, but so would openrouter — a route the transport cannot run).
func TestAIShapePurposesAreThePlansTwelve(t *testing.T) {
	want := map[string]string{
		"chat.techcard_enhance":  AICapabilityChat,
		"chat.techcard_analysis": AICapabilityChat,
		"chat.note_markdown":     AICapabilityChat,
		"chat.email_translate":   AICapabilityChat,
		"chat.design_draft_idea": AICapabilityChat,
		"chat.playground_ideas":  AICapabilityChat,
		"image.generate":         AICapabilityImage,
		"image.cutout":           AICapabilityCutout,
		"image.extend":           AICapabilityEdit,
		"image.inpaint":          AICapabilityEdit,
		"threed":                 AICapabilityThreed,
		"video.generate":         AICapabilityVideo, // B-32: the owner named the video purpose, 28.09
		"vector":                 AICapabilityVector,
	}
	got := AIPurposes()
	if len(got) != len(want) {
		t.Fatalf("AIPurposes() has %d purposes, want %d", len(got), len(want))
	}
	for _, p := range got {
		c, ok := want[p]
		if !ok {
			t.Fatalf("unexpected purpose %q", p)
		}
		if !IsAIPurpose(p) {
			t.Fatalf("%q is listed but IsAIPurpose refuses it", p)
		}
		if AIPurposeCapability(p) != c {
			t.Fatalf("AIPurposeCapability(%q) = %q, want %q", p, AIPurposeCapability(p), c)
		}
	}
	// chat.techcard_operations_draft is retired (O-66, 0378): a route naming it is refused like any word.
	for _, p := range []string{"", "chat", "image.flat", "video", "video.clip", "chat.techcard_enhance ", "chat.techcard_operations_draft"} {
		if IsAIPurpose(p) {
			t.Fatalf("IsAIPurpose(%q) = true", p)
		}
	}
}

// TestAIShapeEveryPurposeHasAProvider. A purpose no provider can serve is a route the panel can never
// save.
//
// MUTATION IT CATCHES: removing `threed` from both fal and meshy, or `vector` from recraft.
func TestAIShapeEveryPurposeHasAProvider(t *testing.T) {
	for _, p := range AIPurposes() {
		c := AIPurposeCapability(p)
		served := false
		for _, k := range AIProviderKeys() {
			if AIProviderServes(k, c) {
				served = true
				break
			}
		}
		if !served {
			t.Fatalf("no provider serves %q (capability %q)", p, c)
		}
	}
}

// TestAIShapeEveryRunKindSpendsUnderAPurpose. The design worker books every attempt under
// AIPurposeOfRunKind(run.Kind); a kind that maps to "" would write ledger rows with no purpose.
//
// MUTATION IT CATCHES: a new DesignRunKind added to DesignRunKinds without a case here (there is no
// default on purpose), or cutout booked as image.generate.
func TestAIShapeEveryRunKindSpendsUnderAPurpose(t *testing.T) {
	want := map[string]string{
		DesignRunKindFlat:      AIPurposeImageGenerate,
		DesignRunKindRender:    AIPurposeImageGenerate,
		DesignRunKindRecolor:   AIPurposeImageGenerate,
		DesignRunKindPattern:   AIPurposeImageGenerate,
		DesignRunKindFreeform:  AIPurposeImageGenerate,
		DesignRunKindCutout:    AIPurposeImageCutout,
		DesignRunKindExtend:    AIPurposeImageExtend,
		DesignRunKindInpaint:   AIPurposeImageInpaint,
		DesignRunKindVideo:     AIPurposeVideoGenerate,
		DesignRunKindThreed:    AIPurposeThreed,
		DesignRunKindVector:    AIPurposeVector,
		DesignRunKindDraftIdea: AIPurposeDesignDraftIdea,
	}
	for _, kind := range DesignRunKinds() {
		p := AIPurposeOfRunKind(kind)
		if !IsAIPurpose(p) {
			t.Fatalf("run kind %q spends under %q, which is not a purpose", kind, p)
		}
		if w, ok := want[kind]; !ok || p != w {
			t.Fatalf("AIPurposeOfRunKind(%q) = %q, want %q", kind, p, w)
		}
	}
	if AIPurposeOfRunKind("") != "" || AIPurposeOfRunKind("hologram") != "" {
		t.Fatal("an unknown run kind must map to no purpose, not to a plausible default")
	}
}

// TestAIShapeLedgerVocabulariesAreClosed pins the status and cost-source vocabularies of 02-PLAN A1.
//
// MUTATION IT CATCHES: a status or source added to the list and not to the Is* switch (FinishCall
// would refuse a status the ledger writer uses), and a renamed literal (`charged-failed`), which the
// report's IN lists would then silently stop counting.
func TestAIShapeLedgerVocabulariesAreClosed(t *testing.T) {
	statuses := []string{"dispatching", "ok", "free", "failed", "charged_failed", "accepted", "unknown"}
	if got := AICallStatuses(); !slices.Equal(got, statuses) {
		t.Fatalf("AICallStatuses() = %v, want %v", got, statuses)
	}
	for _, s := range statuses {
		if !IsAICallStatus(s) {
			t.Fatalf("IsAICallStatus(%q) = false", s)
		}
	}
	sources := []string{"provider", "units", "table", "estimate", "free", "none"}
	if got := AICostSources(); !slices.Equal(got, sources) {
		t.Fatalf("AICostSources() = %v, want %v", got, sources)
	}
	for _, s := range sources {
		if !IsAICostSource(s) {
			t.Fatalf("IsAICostSource(%q) = false", s)
		}
	}
	for _, s := range []string{"", "OK", "backfill", "delivered", "charged-failed"} {
		if IsAICallStatus(s) || IsAICostSource(s) {
			t.Fatalf("%q must be in neither ledger vocabulary", s)
		}
	}
	if AIKeyAPI != "api" || AIKeyAdmin != "admin" {
		t.Fatal("the key kinds are part of the AAD (provider:kind); renaming one orphans every sealed key")
	}
	if AICallErrorSweeper != "sweeper" {
		t.Fatal("the sweeper's error code is the one 02-PLAN A1 names")
	}
}
