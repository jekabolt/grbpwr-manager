package designgen

// noKeySentence is what a route with no key says at the door (CredentialNamer): WHERE a key goes —
// the admin panel's AI providers page, the ONLY place a key is read from since B-33. The env
// variable the sentence once offered as an alternative («or FAL_KEY») is gone from it on purpose: a
// person who follows that advice today sets a variable nobody reads and waits for a door that stays
// shut. envVars is still taken (every caller names its old variable) so the callers did not have to
// change; it no longer reaches the sentence.
//
// It says "no key" for a provider switched off in the panel too: the route only knows that its
// client has no key to send, not why.
func noKeySentence(provider, envVars string) string {
	_ = envVars
	return "no key for " + provider + " — save it in admin → AI providers"
}
