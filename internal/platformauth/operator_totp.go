package platformauth

import (
	"context"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov/keyring"
	"github.com/jekabolt/grbpwr-manager/internal/auth/jwt"
	"github.com/jekabolt/grbpwr-manager/internal/middleware"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/proto/gen/auth"
)

// Operator console second step (wave teams, gap 4; owner 20.11: «приложение на телефоне»). A right
// console password no longer opens the console: it opens a ticket, and the console token is minted
// only after a six-digit code from the operator's authenticator app (TOTP, RFC 6238, via
// github.com/pquerna/otp) or one of the recovery codes. An operator without an app sets one up
// behind a code mailed to their stored address (an attacker holding only the password cannot enrol
// their own app). A password reset by mail never touches the app.
//
// Codes: 30-second steps, ±1 step, six digits, SHA-1 (what every authenticator app reads); a step
// is accepted once (last_step). Guessing: 5 wrong codes burn a ticket, 10 in a row lock the account
// for 15 minutes, and a ticket needs the password (whose own limiters apply). The secret is stored
// AES-256-GCM-sealed under a key derived (HKDF-SHA256) from PLATFORM_TOKEN_PEPPER, bound to the
// operator's id; rotating the pepper therefore removes every enrolled app (each operator sets
// theirs up again behind a mailed code).
const (
	// PurposeOperatorSetup: the mailed code before a first setup (CodeHash purpose).
	PurposeOperatorSetup = "operator_setup"
	// purposeRecovery: the stored form of a recovery code (CodeHash purpose).
	purposeRecovery = "operator_recovery"

	totpIssuer          = "lekalo"
	totpPeriod          = 30
	totpSkew            = 1
	recoveryCodeCount   = 10
	recoveryCodeLen     = 8
	recoveryAlphabet    = "abcdefghjkmnpqrstuvwxyz23456789" // no 0/o, 1/l/i
	operatorSecretInfo  = "lekalo operator totp v1"
	operatorSecretBytes = 20 // 160-bit secret, RFC 4226's recommendation
)

// SecondStepPolicy bounds guessing of the operator second step.
var SecondStepPolicy = platformstore.SecondStepPolicy{
	TicketTTL:          10 * time.Minute,
	MaxTicketAttempts:  5,
	MaxFailures:        10,
	LockFor:            15 * time.Minute,
	MaxSetupLettersPer: 5,
}

// Answers of the second step.
func errTicketGone() error { return status.Error(codes.FailedPrecondition, "sign_in_again") }
func errLocked() error     { return status.Error(codes.ResourceExhausted, "locked") }

// operatorRing is the AEAD that seals operator app secrets, derived from the pepper.
func (s *Service) operatorRing() (*keyring.Ring, error) {
	key, err := hkdf.Key(sha256.New, []byte(s.pepper), nil, operatorSecretInfo, 32)
	if err != nil {
		return nil, fmt.Errorf("platformauth: derive operator key: %w", err)
	}
	r, err := keyring.New(base64.StdEncoding.EncodeToString(key))
	clear(key)
	if err != nil {
		return nil, errors.New("platformauth: operator key")
	}
	return r, nil
}

func operatorAAD(userID int64) string { return "operator_totp:" + strconv.FormatInt(userID, 10) }

// startOperatorSecondStep answers a right console password: a ticket and what it needs.
func (s *Service) startOperatorSecondStep(ctx context.Context, u platformstore.User, now time.Time) (*auth.SignInResponse, error) {
	ticket, ticketHash, err := NewToken()
	if err != nil {
		s.log(ctx, "ticket", err)
		return nil, errInternal()
	}
	code, err := NewCode()
	if err != nil {
		s.log(ctx, "setup code", err)
		return nil, errInternal()
	}
	res, err := s.d.Store.StartOperatorSecondStep(ctx, platformstore.StartSecondStep{
		UserID: u.ID, CredGen: u.CredGen, TicketHash: ticketHash,
		MailCodeHash: CodeHash(s.pepper, PurposeOperatorSetup, u.ID, code),
		SetupLetter:  platformstore.Letter{Code: code},
		Now:          now,
	}, SecondStepPolicy)
	if errors.Is(err, platformstore.ErrTokenInvalid) || errors.Is(err, platformstore.ErrUserNotFound) {
		return nil, errAuth()
	}
	if err != nil {
		s.log(ctx, "start second step", err)
		return nil, errInternal()
	}
	resp := &auth.SignInResponse{Ticket: ticket, SecondStep: auth.OperatorSecondStep_OPERATOR_SECOND_STEP_CODE}
	if res.Kind == platformstore.SecondStepSetup {
		resp.SecondStep = auth.OperatorSecondStep_OPERATOR_SECOND_STEP_SETUP
		resp.SetupLetterSent = res.LetterQueued
	}
	s.audit(ctx, u.ID, 0, "signin_password", map[string]string{"target": "console", "step": res.Kind})
	return resp, nil
}

// OperatorSetupMail checks the mailed setup code and answers the new app's key and link.
func (s *Service) OperatorSetupMail(ctx context.Context, req *auth.OperatorSetupMailRequest) (*auth.OperatorSetupMailResponse, error) {
	if err := s.secondStepGate(ctx); err != nil {
		return nil, err
	}
	code, ok := NormalizeCode(req.GetCode())
	ring, rerr := s.operatorRing()
	if rerr != nil {
		s.log(ctx, "operator ring", rerr)
		return nil, errInternal()
	}
	var secret, url string
	_, err := s.d.Store.JudgeSecondStep(ctx, HashToken(req.GetTicket()), platformstore.SecondStepSetup, s.now(), SecondStepPolicy,
		func(st platformstore.SecondStepState) (platformstore.SecondStepVerdict, error) {
			if st.Ticket.MailOKAt.Valid {
				return platformstore.SecondStepVerdict{}, platformstore.ErrTokenInvalid
			}
			if !ok || !st.Ticket.MailCodeHash.Valid ||
				!CodeMatches(s.pepper, PurposeOperatorSetup, st.User.ID, code, st.Ticket.MailCodeHash.String) {
				return platformstore.SecondStepVerdict{Wrong: true}, nil
			}
			key, err := totp.Generate(totp.GenerateOpts{
				Issuer: totpIssuer, AccountName: st.User.Email.String, Period: totpPeriod,
				SecretSize: operatorSecretBytes, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1,
			})
			if err != nil {
				return platformstore.SecondStepVerdict{}, fmt.Errorf("platformauth: totp key: %w", err)
			}
			sealed, err := ring.Seal(key.Secret(), operatorAAD(st.User.ID))
			if err != nil {
				return platformstore.SecondStepVerdict{}, fmt.Errorf("platformauth: seal totp: %w", err)
			}
			secret, url = key.Secret(), key.URL()
			return platformstore.SecondStepVerdict{MailOK: true, PendingSecretSealed: sealed}, nil
		})
	if err := s.secondStepErr(ctx, "setup mail", err); err != nil {
		return nil, err
	}
	return &auth.OperatorSetupMailResponse{Secret: secret, OtpauthUrl: url}, nil
}

// OperatorSetupConfirm confirms the new app with one of its codes: the app is stored, ten recovery
// codes are made (answered once) and the console token is minted.
func (s *Service) OperatorSetupConfirm(ctx context.Context, req *auth.OperatorSetupConfirmRequest) (*auth.OperatorSetupConfirmResponse, error) {
	if err := s.secondStepGate(ctx); err != nil {
		return nil, err
	}
	code, ok := NormalizeCode(req.GetCode())
	ring, rerr := s.operatorRing()
	if rerr != nil {
		s.log(ctx, "operator ring", rerr)
		return nil, errInternal()
	}
	sid, err := NewSessionID()
	if err != nil {
		s.log(ctx, "session id", err)
		return nil, errInternal()
	}
	ctx = withSessionClient(ctx)
	now := s.now()
	var codesOut []string
	out, err := s.d.Store.JudgeSecondStep(ctx, HashToken(req.GetTicket()), platformstore.SecondStepSetup, now, SecondStepPolicy,
		func(st platformstore.SecondStepState) (platformstore.SecondStepVerdict, error) {
			if !st.Ticket.MailOKAt.Valid || len(st.Ticket.PendingSecretSealed) == 0 {
				return platformstore.SecondStepVerdict{}, platformstore.ErrTokenInvalid
			}
			secret, err := ring.Open(st.Ticket.PendingSecretSealed, operatorAAD(st.User.ID))
			if err != nil {
				return platformstore.SecondStepVerdict{}, fmt.Errorf("platformauth: open pending totp: %w", err)
			}
			step, match := matchTOTP(secret, code, ok, now, 0)
			if !match {
				return platformstore.SecondStepVerdict{Wrong: true}, nil
			}
			plain, hashes, err := s.newRecoveryCodes(st.User.ID)
			if err != nil {
				return platformstore.SecondStepVerdict{}, err
			}
			codesOut = plain
			return platformstore.SecondStepVerdict{Enroll: true, EnrollStep: step, RecoveryHashes: hashes, SessionSID: sid}, nil
		})
	if err := s.secondStepErr(ctx, "setup confirm", err); err != nil {
		return nil, err
	}
	tok, err := s.mintConsole(ctx, out.User, sid, now)
	if err != nil {
		return nil, err
	}
	s.audit(ctx, out.User.ID, 0, "operator_app_setup", nil)
	s.audit(ctx, out.User.ID, 0, "signin", map[string]string{"target": "console"})
	return &auth.OperatorSetupConfirmResponse{Token: tok, RecoveryCodes: codesOut}, nil
}

// OperatorVerify is the second step of a console sign-in: an app code or a recovery code.
func (s *Service) OperatorVerify(ctx context.Context, req *auth.OperatorVerifyRequest) (*auth.OperatorVerifyResponse, error) {
	if err := s.secondStepGate(ctx); err != nil {
		return nil, err
	}
	raw := req.GetCode()
	code, isApp := NormalizeCode(raw)
	recovery, isRecovery := normalizeRecovery(raw)
	ring, rerr := s.operatorRing()
	if rerr != nil {
		s.log(ctx, "operator ring", rerr)
		return nil, errInternal()
	}
	sid, err := NewSessionID()
	if err != nil {
		s.log(ctx, "session id", err)
		return nil, errInternal()
	}
	ctx = withSessionClient(ctx)
	now := s.now()
	usedRecovery := false
	out, err := s.d.Store.JudgeSecondStep(ctx, HashToken(req.GetTicket()), platformstore.SecondStepCode, now, SecondStepPolicy,
		func(st platformstore.SecondStepState) (platformstore.SecondStepVerdict, error) {
			usedRecovery = false
			if isApp {
				secret, err := ring.Open(st.TOTP.SecretSealed, operatorAAD(st.User.ID))
				if err != nil {
					return platformstore.SecondStepVerdict{}, fmt.Errorf("platformauth: open totp: %w", err)
				}
				step, match := matchTOTP(secret, code, true, now, st.TOTP.LastStep)
				if !match {
					return platformstore.SecondStepVerdict{Wrong: true}, nil
				}
				return platformstore.SecondStepVerdict{AcceptStep: step, SessionSID: sid}, nil
			}
			if !isRecovery {
				return platformstore.SecondStepVerdict{Wrong: true}, nil
			}
			want, _ := hex.DecodeString(CodeHash(s.pepper, purposeRecovery, st.User.ID, recovery))
			var hit int64
			for _, r := range st.Recovery {
				got, err := hex.DecodeString(r.CodeHash)
				if err == nil && hmac.Equal(got, want) && hit == 0 {
					hit = r.ID
				}
			}
			if hit == 0 {
				return platformstore.SecondStepVerdict{Wrong: true}, nil
			}
			usedRecovery = true
			return platformstore.SecondStepVerdict{UseRecoveryID: hit, SessionSID: sid}, nil
		})
	if err := s.secondStepErr(ctx, "verify", err); err != nil {
		return nil, err
	}
	tok, err := s.mintConsole(ctx, out.User, sid, now)
	if err != nil {
		return nil, err
	}
	resp := &auth.OperatorVerifyResponse{Token: tok}
	detail := map[string]string{"target": "console"}
	if usedRecovery {
		resp.RecoveryCodesLeft = int32(max(out.RecoveryLeft, 0))
		detail["with"] = "recovery"
	}
	s.audit(ctx, out.User.ID, 0, "signin", detail)
	return resp, nil
}

// secondStepGate: the service is usable and the caller's IP is under the code limiter.
func (s *Service) secondStepGate(ctx context.Context) error {
	if err := s.usable(); err != nil {
		return err
	}
	if !s.codeIP.Allow(middleware.GetClientIP(ctx)) {
		return errBusy()
	}
	return nil
}

func (s *Service) secondStepErr(ctx context.Context, what string, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, platformstore.ErrCodeWrong):
		return errCode()
	case errors.Is(err, platformstore.ErrSecondStepLocked):
		return errLocked()
	case errors.Is(err, platformstore.ErrTokenInvalid), errors.Is(err, platformstore.ErrUserNotFound),
		errors.Is(err, platformstore.ErrCredentialsChanged):
		return errTicketGone()
	default:
		s.log(ctx, "second step "+what, err)
		return errInternal()
	}
}

// mintConsole mints the console token of a session the store just opened (claim s2). A failed mint
// revokes the session.
func (s *Service) mintConsole(ctx context.Context, u platformstore.User, sid string, now time.Time) (string, error) {
	tok, err := jwt.NewTeamTokenAt(s.d.JWT, s.d.TeamTTL, jwt.TeamClaims{
		Handle: u.Handle, UserID: u.ID, SessionID: sid, Audience: jwt.AudConsole, SecondStep: true,
	}, now)
	if err != nil {
		s.log(ctx, "mint console token", err)
		if rerr := s.d.Store.RevokeSession(ctx, sid, now); rerr != nil {
			s.log(ctx, "revoke session", rerr)
		}
		return "", errInternal()
	}
	return tok, nil
}

// matchTOTP finds the step (within ±totpSkew of now) whose code equals code, refusing steps at or
// before after (replay). Every candidate is compared in constant time.
func matchTOTP(secret, code string, wellFormed bool, now time.Time, after int64) (int64, bool) {
	if !wellFormed {
		return 0, false
	}
	cur := now.Unix() / totpPeriod
	var found int64
	for d := int64(-totpSkew); d <= totpSkew; d++ {
		step := cur + d
		want, err := totp.GenerateCodeCustom(secret, time.Unix(step*totpPeriod, 0).UTC(), totp.ValidateOpts{
			Period: totpPeriod, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1,
		})
		if err != nil {
			return 0, false
		}
		if hmac.Equal([]byte(want), []byte(code)) && step > after && found == 0 {
			found = step
		}
	}
	return found, found != 0
}

// newRecoveryCodes makes the ten recovery codes (xxxx-xxxx, 8 symbols of a 31-letter alphabet, ~39
// bits each) and their stored hashes.
func (s *Service) newRecoveryCodes(userID int64) (plain, hashes []string, err error) {
	for range recoveryCodeCount {
		raw, err := randomFrom(recoveryAlphabet, recoveryCodeLen)
		if err != nil {
			return nil, nil, err
		}
		plain = append(plain, raw[:4]+"-"+raw[4:])
		hashes = append(hashes, CodeHash(s.pepper, purposeRecovery, userID, raw))
	}
	return plain, hashes, nil
}

func randomFrom(alphabet string, n int) (string, error) {
	limit := 256 - 256%len(alphabet)
	out := make([]byte, 0, n)
	var b [1]byte
	for len(out) < n {
		if _, err := rand.Read(b[:]); err != nil {
			return "", fmt.Errorf("platformauth: random recovery code: %w", err)
		}
		if int(b[0]) < limit {
			out = append(out, alphabet[int(b[0])%len(alphabet)])
		}
	}
	return string(out), nil
}

// normalizeRecovery lowercases a typed recovery code and strips spaces and dashes.
func normalizeRecovery(s string) (string, bool) {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r == ' ' || r == '-':
			continue
		case strings.ContainsRune(recoveryAlphabet, r):
			b.WriteRune(r)
		default:
			return "", false
		}
	}
	out := b.String()
	return out, len(out) == recoveryCodeLen
}
