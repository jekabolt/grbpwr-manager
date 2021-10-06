package app

import (
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/jwtauth/v5"
	"github.com/go-chi/render"
	"github.com/lestrrat-go/jwx/jwt"
	"github.com/rs/zerolog/log"
)

const (
	RefreshTokenSub = "refresh"
)

func (s *Server) GetJWT() (*AuthResponse, error) {
	_, ts, err := s.JWTAuth.Encode(map[string]interface{}{
		"iss": "backend.grbpwr.com",
		"exp": time.Now().Add(time.Minute * 15).Unix(),
	})
	if err != nil {
		return nil, err
	}

	_, rts, err := s.JWTAuth.Encode(map[string]interface{}{
		"sub": RefreshTokenSub,
		"exp": time.Now().Add(time.Hour * 24).Unix(),
	})
	if err != nil {
		return nil, err
	}

	return &AuthResponse{
		AccessToken:  ts,
		RefreshToken: rts,
	}, nil
}

func (s *Server) auth(w http.ResponseWriter, r *http.Request) {
	ar := &AuthRequest{}

	if err := render.Bind(r, ar); err != nil {
		log.Error().Err(err).Msgf("auth:render.Bind [%v]", err.Error())
		render.Render(w, r, ErrInvalidRequest(err))
		return
	}

	authorized := false

	// get token by password
	if ar.Password != "" {
		err := s.ValidateAdminSecret(ar)
		if err != nil {
			log.Error().Err(err).Msgf("auth:ValidateAdminSecretd [%v]", err.Error())
			render.Render(w, r, ErrUnauthorizedError(err))
			return
		}
		authorized = true
	}

	// get token by refresh token
	if ar.RefreshToken != "" {
		rt, err := jwtauth.VerifyToken(s.JWTAuth, ar.RefreshToken)
		if err != nil {
			log.Error().Err(err).Msgf("auth:jwtauth.VerifyToken [%v]", err.Error())
			render.Render(w, r, ErrInvalidRequest(err))
			return
		}
		if rt.Subject() == RefreshTokenSub {
			authorized = true
		}
	}

	if authorized {
		token, err := s.GetJWT()
		if err != nil {
			log.Error().Err(err).Msgf("auth:GetJWT [%v]", err.Error())
			render.Render(w, r, ErrInternalServerError(err))
			return
		}
		render.Render(w, r, NewAuthResponse(token))
		return
	}

	render.Render(w, r, ErrUnauthorizedError(fmt.Errorf("passowrd or refresh token is invalid")))
	return

}

type AuthRequest struct {
	Password     string `json:"password"`
	RefreshToken string `json:"refreshToken,omitempty"`
}

func (s *Server) ValidateAdminSecret(ar *AuthRequest) error {
	if s.AdminSecret != ar.Password {
		return fmt.Errorf("password not match")
	}
	return nil
}

func (a *AuthRequest) Validate() error {
	if a.Password == "" && a.RefreshToken == "" {
		return fmt.Errorf("nor password and refresh token was send")
	}
	return nil
}

func (a *AuthRequest) Bind(r *http.Request) error {
	return a.Validate()
}

func (s *Server) Authenticator(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, _, err := jwtauth.FromContext(r.Context())

		if err != nil {
			log.Error().Err(err).Msgf("Authenticator:jwtauth.FromContext [%v]", err.Error())
			render.Render(w, r, ErrUnauthorizedError(err))
			return
		}

		if token == nil || jwt.Validate(token) != nil {
			log.Error().Err(err).Msgf("Authenticator:jwt.Validate [%v]", err.Error())
			render.Render(w, r, ErrUnauthorizedError(err))
			return
		}

		if token.Subject() == RefreshTokenSub {
			render.Render(w, r, ErrUnauthorizedError(fmt.Errorf("use access token instead")))
			return
		}

		next.ServeHTTP(w, r)
	})
}
