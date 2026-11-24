package teamfair

import "strings"

// LaneOf is the lane of a gRPC method (full name "/pkg.Service/Method"). Unknown = Light.
//
// Heavy: holds a connection and the vCPU for seconds — archive import/export, uploads with image
// or model processing, vector import / picture split, season clone, accounting exports.
// AI: waits on an AI provider for seconds to a minute.
// A switch, not a map: no package-level state (tools/tenantgate G4).
func LaneOf(fullMethod string) Lane {
	m := fullMethod
	if i := strings.LastIndexByte(m, '/'); i >= 0 {
		m = m[i+1:]
	}
	switch m {
	case "ExportTechCardArchive", "CommitTechCardImport", "ApplyTechCardImportColorways",
		"UploadContentImage", "UploadContentVideo", "UploadContentModel", "UploadContentVector",
		"UploadPattern", "ImportDesignVector", "SplitDesignPicture", "CopyLibraryFileToMedia",
		"CloneStyleForSeason", "ExportJpkV7M", "ExportOssReturn", "ImportBankCsv":
		return Heavy
	case "AnalyzeTechCardConstruction", "ClassifyPatternPages", "DraftDesignIdea", "DraftFittingEdits",
		"EnhanceText", "GenerateDesignJoins", "GenerateDesignQuiz", "SuggestAssemblySkeleton",
		"SuggestCallouts", "SuggestDesignParts", "SuggestDesignPartsCard", "SuggestPatternPieces",
		"SuggestPrompts", "SuggestTechCardSeamHints", "TranslateFields", "AutoTranslateEmailCampaign",
		"FormatLibraryNoteMarkdown":
		return AI
	}
	return Light
}
