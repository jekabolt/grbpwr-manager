package auth

import "net/http"

// SignupChallengeHandler is GET /api/auth/signup-challenge (X6): the sign-up form's invisible
// proof-of-work challenge. nil when teams mode is off or the identity service failed to build.
func (s *Server) SignupChallengeHandler() http.Handler {
	if s.platform.svc == nil {
		return nil
	}
	return s.platform.svc.ChallengeHandler()
}
