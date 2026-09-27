package designgen

// noKeySentence is what a route with no key says at the door (CredentialNamer): WHERE a key goes —
// the admin panel's AI providers page, since keys are read through the AI providers registry — and
// the env variable that still works as the fallback. The variable stays in the sentence: it is what
// a deployment without a stored key reads, and an operator on such a deployment must still be told
// its exact name.
//
// It says "no key" for a provider switched off in the panel too: the route only knows that its
// client has no key to send, not why.
func noKeySentence(provider, envVars string) string {
	return "no key for " + provider + " — set it in admin → AI providers (or " + envVars + ")"
}
