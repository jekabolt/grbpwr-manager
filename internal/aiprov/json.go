package aiprov

import "strings"

// ExtractJSONObject returns the OUTERMOST {…} of a model's answer — from the first '{' to the last
// '}' — after stripping a markdown fence, and whether one was found.
//
// WHY IT LIVES HERE. A provider with no JSON mode on the wire (Anthropic: the transport only asks for
// JSON in the system prompt) hands back text that may carry a fence or a sentence around the object;
// the transport extracts the object before the caller parses it. The translate service did the same
// on its own (translate.extractJSONObject, now a delegate). Two copies of this cut are two opinions
// about the same answer: one paid path accepting what its neighbour rejects.
//
// ⚠ THE CUT IS FIRST '{' TO LAST '}', NOT A BALANCED SCAN, AND THAT IS KEPT ON PURPOSE. It is the
// body translate has always run (moved verbatim), and the design draft's parser
// (apisrv/admin: designExtractJSONObject) is a deliberate twin of it — a balanced, string-aware scan
// here would make the same model answer parse on one path and fail on the other. Prose AFTER the
// object that itself contains a '}' defeats it; the caller's json.Unmarshal then refuses the result,
// which is a loud failure, never a silently wrong object.
func ExtractJSONObject(text string) (string, bool) {
	s := strings.TrimSpace(text)
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```")
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = s[i+1:] // the fence's language tag line («json»)
		}
		if j := strings.LastIndex(s, "```"); j >= 0 {
			s = s[:j]
		}
		s = strings.TrimSpace(s)
	}
	start := strings.IndexByte(s, '{')
	end := strings.LastIndexByte(s, '}')
	if start < 0 || end < 0 || end < start {
		return "", false
	}
	return s[start : end+1], true
}
