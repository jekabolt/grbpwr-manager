package aiprov

import "github.com/jekabolt/grbpwr-manager/internal/entity"

// Purpose groups — the admin → AI providers panel's headings (AiPurposeInfo.group).
const (
	PurposeGroupChat   = "chat"
	PurposeGroupImages = "images"
	PurposeGroup3D     = "3d"
)

// Purpose is one row of the panel's purposes catalogue: what a call is FOR, in words a person reads
// beside its route. Key is the ai_route.purpose / ai_usage_event.purpose word (entity.AIPurpose*);
// Capability is what its candidates must serve and is entity.AIPurposeCapability(Key), repeated here
// so the table reads whole — purposes_test.go holds the two together.
//
// Labels and hints are lowercase English and name WHERE IN THE ADMIN the purpose runs, because that
// is the question a person asks when a route or a bill line names it.
type Purpose struct {
	Key, Label, Hint, Group, Capability string
}

// purposes — one row per purpose seeded in 0373 (ai_route) and not retired since (0378 deletes the
// operations draft's route with its feature, O-66; 0384 seeds video.generate → runblob, B-32), in
// entity.AIPurposes()'s order. The 3d group carries the video purpose too: the panel heads it
// «3d & video» (the client's label; the group KEY stays `3d` so older clients keep their heading).
var purposes = []Purpose{
	{entity.AIPurposeTechCardEnhance, "text enhance",
		"the ✦ button on a tech card's text fields", PurposeGroupChat, entity.AICapabilityChat},
	{entity.AIPurposeTechCardAnalysis, "construction analysis",
		"tech card → analyse the construction", PurposeGroupChat, entity.AICapabilityChat},
	{entity.AIPurposeNoteMarkdown, "note to markdown",
		"the ✦ button in a library note", PurposeGroupChat, entity.AICapabilityChat},
	{entity.AIPurposeEmailTranslate, "email translation",
		"email campaigns → auto-translate", PurposeGroupChat, entity.AICapabilityChat},
	{entity.AIPurposeDesignDraftIdea, "draft the idea",
		"design → moodboard: draft the idea from its pictures and words", PurposeGroupChat, entity.AICapabilityChat},
	{entity.AIPurposePlaygroundIdeas, "prompt ideas",
		"the ideas ▾ button beside a playground prompt", PurposeGroupChat, entity.AICapabilityChat},
	{entity.AIPurposeImageGenerate, "design images",
		"flat, render, recolour, pattern, playground tiles", PurposeGroupImages, entity.AICapabilityImage},
	{entity.AIPurposeImageCutout, "background removal",
		"cutout: a picture without its background", PurposeGroupImages, entity.AICapabilityCutout},
	{entity.AIPurposeImageExtend, "extend image",
		"playground: grow a picture past its edges", PurposeGroupImages, entity.AICapabilityEdit},
	{entity.AIPurposeImageInpaint, "retouch a zone",
		"playground: repaint a painted zone of a picture", PurposeGroupImages, entity.AICapabilityEdit},
	{entity.AIPurposeThreed, "3d build",
		"design → 3d: the garment model built from its flats", PurposeGroup3D, entity.AICapabilityThreed},
	{entity.AIPurposeVideoGenerate, "video clip",
		"playground → image to video: a short clip from one picture of the card", PurposeGroup3D, entity.AICapabilityVideo},
	{entity.AIPurposeVector, "vector",
		"design → vector: the flat as a vector drawing", PurposeGroupImages, entity.AICapabilityVector},
}

// Purposes returns the catalogue in the panel's fixed order. A copy on every call.
func Purposes() []Purpose {
	return append([]Purpose(nil), purposes...)
}

// PurposeInfo finds one purpose by key.
func PurposeInfo(key string) (Purpose, bool) {
	for _, p := range purposes {
		if p.Key == key {
			return p, true
		}
	}
	return Purpose{}, false
}
