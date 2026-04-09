package frontend

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	v "github.com/asaskevich/govalidator"
	"github.com/google/uuid"
	"github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/auth/jwt"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/middleware"
	"github.com/jekabolt/grbpwr-manager/internal/storefront"
	"github.com/jekabolt/grbpwr-manager/internal/storefront/tokenhash"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	pb_frontend "github.com/jekabolt/grbpwr-manager/proto/gen/frontend"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const maxSavedAddresses = 20

func getBearerToken(ctx context.Context) (string, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", fmt.Errorf("missing metadata")
	}
	for _, key := range []string{"authorization", auth.AuthMetadataKey} {
		vals := md.Get(key)
		if len(vals) == 0 {
			continue
		}
		v := strings.TrimSpace(vals[0])
		return strings.TrimPrefix(strings.TrimPrefix(v, "Bearer "), "bearer "), nil
	}
	return "", fmt.Errorf("missing authorization")
}

func normalizeEmail(e string) string {
	return strings.ToLower(strings.TrimSpace(e))
}

func randomNumericOTP() (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	n := binary.BigEndian.Uint32(b[:]) % 1000000
	return fmt.Sprintf("%06d", n), nil
}

func randomMagicToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func (s *Server) requireStorefrontAuth() error {
	if s.storefront == nil {
		return status.Error(codes.FailedPrecondition, "storefront account API is not configured")
	}
	return nil
}

func (s *Server) storefrontEmailFromAccess(ctx context.Context) (string, error) {
	if err := s.requireStorefrontAuth(); err != nil {
		return "", err
	}
	tok, err := getBearerToken(ctx)
	if err != nil || tok == "" {
		return "", status.Error(codes.Unauthenticated, "missing access token")
	}
	sub, jti, _, err := jwt.VerifyTokenFull(s.storefront.accessJwtAuth, tok, s.storefront.accessExpectations)
	if err != nil || sub == "" {
		return "", status.Error(codes.Unauthenticated, "invalid or expired access token")
	}
	if s.storefront.accessJtiRevocationEnabled && jti != "" {
		denylisted, err := s.repo.StorefrontAccount().IsJtiDenylisted(ctx, jti)
		if err != nil {
			slog.Default().ErrorContext(ctx, "jti denylist check failed", slog.String("err", err.Error()))
			return "", status.Error(codes.Internal, "auth check failed")
		}
		if denylisted {
			return "", status.Error(codes.Unauthenticated, "invalid or expired access token")
		}
	}
	return normalizeEmail(sub), nil
}

func (s *Server) issueAccessAndRefresh(ctx context.Context, email string) (accessToken, refreshToken string, accessExp time.Time, acc *entity.StorefrontAccount, err error) {
	if err := s.requireStorefrontAuth(); err != nil {
		return "", "", time.Time{}, nil, err
	}
	acc, err = s.repo.StorefrontAccount().GetOrCreateAccountByEmail(ctx, email)
	if err != nil {
		return "", "", time.Time{}, nil, fmt.Errorf("account: %w", err)
	}
	rawRefresh, err := randomMagicToken()
	if err != nil {
		return "", "", time.Time{}, nil, err
	}
	now := time.Now().UTC()
	h := tokenhash.Hash(s.storefront.refreshPepper, rawRefresh)
	fid := uuid.New().String()
	expAt := now.Add(s.storefront.refreshTTL)
	if _, err := s.repo.StorefrontAccount().InsertRefreshToken(ctx, acc.ID, h, fid, expAt); err != nil {
		return "", "", time.Time{}, nil, fmt.Errorf("refresh token: %w", err)
	}
	accessExp = now.Add(s.storefront.accessTTL)
	accessToken, err = jwt.NewTokenWithSubjectOptsAt(s.storefront.accessJwtAuth, s.storefront.accessTTL, email, s.storefront.accessIssueOpts, now)
	if err != nil {
		return "", "", time.Time{}, nil, err
	}
	return accessToken, rawRefresh, accessExp, acc, nil
}

// RequestAccountLogin queues OTP + magic link email.
func (s *Server) RequestAccountLogin(ctx context.Context, req *pb_frontend.RequestAccountLoginRequest) (*pb_frontend.RequestAccountLoginResponse, error) {
	if err := s.requireStorefrontAuth(); err != nil {
		return nil, err
	}
	email := normalizeEmail(req.GetEmail())
	if email == "" || !v.IsEmail(email) {
		return nil, status.Error(codes.InvalidArgument, "valid email is required")
	}
	ip := middleware.GetClientIP(ctx)
	if err := s.rateLimiter.CheckAccountLoginRequest(ip, email); err != nil {
		return nil, status.Error(codes.ResourceExhausted, err.Error())
	}
	otp, err := randomNumericOTP()
	if err != nil {
		slog.Default().ErrorContext(ctx, "account login otp", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't start login")
	}
	magic, err := randomMagicToken()
	if err != nil {
		slog.Default().ErrorContext(ctx, "account login magic", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't start login")
	}
	otpHash := tokenhash.Hash(s.storefront.loginPepper, "otp:"+email+":"+otp)
	magicHash := tokenhash.Hash(s.storefront.loginPepper, "magic:"+magic)
	exp := time.Now().UTC().Add(s.storefront.loginChallengeTTL)
	if err := s.repo.StorefrontAccount().InsertLoginChallenge(ctx, email, otpHash, magicHash, exp); err != nil {
		slog.Default().ErrorContext(ctx, "insert login challenge", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't start login")
	}
	magicURL, err := url.Parse(s.storefront.magicLinkBaseURL)
	if err != nil {
		return nil, status.Error(codes.Internal, "invalid magic_link_base_url")
	}
	q := magicURL.Query()
	q.Set("token", magic)
	magicURL.RawQuery = q.Encode()
	if err := s.mailer.QueueAccountLogin(ctx, s.repo, email, otp, magicURL.String()); err != nil {
		slog.Default().ErrorContext(ctx, "queue account login mail", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't send login email")
	}
	return &pb_frontend.RequestAccountLoginResponse{}, nil
}

// VerifyAccountLoginCode completes login with OTP.
func (s *Server) VerifyAccountLoginCode(ctx context.Context, req *pb_frontend.VerifyAccountLoginCodeRequest) (*pb_frontend.VerifyAccountLoginResponse, error) {
	if err := s.requireStorefrontAuth(); err != nil {
		return nil, err
	}
	email := normalizeEmail(req.GetEmail())
	code := strings.TrimSpace(req.GetCode())
	if email == "" || code == "" {
		return nil, status.Error(codes.InvalidArgument, "email and code are required")
	}
	ip := middleware.GetClientIP(ctx)
	if err := s.rateLimiter.CheckAccountVerify(ip, email, ""); err != nil {
		return nil, status.Error(codes.ResourceExhausted, err.Error())
	}
	if _, err := s.repo.StorefrontAccount().ConsumeLoginChallengeOTP(ctx, email, code, s.storefront.loginPepper); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.Unauthenticated, "invalid or expired code")
		}
		slog.Default().ErrorContext(ctx, "consume otp", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't verify code")
	}
	return s.finishLogin(ctx, email)
}

// VerifyAccountMagicLink completes login with magic token.
func (s *Server) VerifyAccountMagicLink(ctx context.Context, req *pb_frontend.VerifyAccountMagicLinkRequest) (*pb_frontend.VerifyAccountLoginResponse, error) {
	if err := s.requireStorefrontAuth(); err != nil {
		return nil, err
	}
	tok := strings.TrimSpace(req.GetToken())
	if tok == "" {
		return nil, status.Error(codes.InvalidArgument, "token is required")
	}
	ip := middleware.GetClientIP(ctx)
	magicSum := sha256.Sum256([]byte(tok))
	magicKey := hex.EncodeToString(magicSum[:])
	if err := s.rateLimiter.CheckAccountVerify(ip, "", magicKey); err != nil {
		return nil, status.Error(codes.ResourceExhausted, err.Error())
	}
	email, err := s.repo.StorefrontAccount().ConsumeLoginChallengeMagic(ctx, tok, s.storefront.loginPepper)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.Unauthenticated, "invalid or expired link")
		}
		slog.Default().ErrorContext(ctx, "consume magic", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't verify link")
	}
	return s.finishLogin(ctx, normalizeEmail(email))
}

func (s *Server) finishLogin(ctx context.Context, email string) (*pb_frontend.VerifyAccountLoginResponse, error) {
	access, refresh, exp, acc, err := s.issueAccessAndRefresh(ctx, email)
	if err != nil {
		slog.Default().ErrorContext(ctx, "issue tokens", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't complete login")
	}
	pbAcc, err := dto.EntityStorefrontAccountToPb(acc, nil)
	if err != nil {
		return nil, status.Error(codes.Internal, "can't build account")
	}
	return &pb_frontend.VerifyAccountLoginResponse{
		AccessToken:     access,
		RefreshToken:    refresh,
		AccessExpiresAt: timestamppb.New(exp),
		Account:         pbAcc,
	}, nil
}

// RefreshAccountSession rotates refresh token and issues new access token.
func (s *Server) RefreshAccountSession(ctx context.Context, req *pb_frontend.RefreshAccountSessionRequest) (*pb_frontend.RefreshAccountSessionResponse, error) {
	if err := s.requireStorefrontAuth(); err != nil {
		return nil, err
	}
	raw := strings.TrimSpace(req.GetRefreshToken())
	if raw == "" {
		return nil, status.Error(codes.InvalidArgument, "refresh_token is required")
	}
	ip := middleware.GetClientIP(ctx)
	if err := s.rateLimiter.CheckAccountVerify(ip, "", ""); err != nil {
		return nil, status.Error(codes.ResourceExhausted, err.Error())
	}
	now := time.Now().UTC()
	newRaw, email, err := s.repo.StorefrontAccount().RotateRefreshToken(ctx, raw, s.storefront.refreshPepper, s.storefront.refreshTTL, now)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.Unauthenticated, "invalid refresh token")
		}
		if errors.Is(err, storefront.ErrRefreshTokenRevoked) || errors.Is(err, storefront.ErrRefreshTokenExpired) {
			return nil, status.Error(codes.Unauthenticated, err.Error())
		}
		slog.Default().ErrorContext(ctx, "refresh rotate", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't refresh session")
	}
	accessExp := now.Add(s.storefront.accessTTL)
	access, err := jwt.NewTokenWithSubjectOptsAt(s.storefront.accessJwtAuth, s.storefront.accessTTL, email, s.storefront.accessIssueOpts, now)
	if err != nil {
		return nil, status.Error(codes.Internal, "can't issue access token")
	}
	return &pb_frontend.RefreshAccountSessionResponse{
		AccessToken:     access,
		RefreshToken:    newRaw,
		AccessExpiresAt: timestamppb.New(accessExp),
	}, nil
}

// RevokeAccountSession logs out this client: JTI denylist for the access token and revoke this
// refresh token's family. Requires Bearer token and refresh_token.
func (s *Server) RevokeAccountSession(ctx context.Context, req *pb_frontend.RevokeAccountSessionRequest) (*pb_frontend.RevokeAccountSessionResponse, error) {
	if err := s.requireStorefrontAuth(); err != nil {
		return nil, err
	}
	rawRefresh := strings.TrimSpace(req.GetRefreshToken())
	if rawRefresh == "" {
		return nil, status.Error(codes.InvalidArgument, "refresh_token is required")
	}
	tok, err := getBearerToken(ctx)
	if err != nil || tok == "" {
		return nil, status.Error(codes.Unauthenticated, "missing access token")
	}
	sub, jti, expAt, err := jwt.VerifyTokenFull(s.storefront.accessJwtAuth, tok, s.storefront.accessExpectations)
	if err != nil || sub == "" {
		return nil, status.Error(codes.Unauthenticated, "invalid or expired access token")
	}
	acc, err := s.repo.StorefrontAccount().GetAccountByEmail(ctx, normalizeEmail(sub))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "account not found")
		}
		return nil, status.Error(codes.Internal, "can't load account")
	}
	if jti != "" {
		denylistExp := expAt
		if denylistExp.IsZero() {
			denylistExp = time.Now().UTC().Add(s.storefront.accessTTL)
		}
		if err := s.repo.StorefrontAccount().InsertJtiDenylist(ctx, jti, acc.ID, denylistExp); err != nil {
			slog.Default().ErrorContext(ctx, "insert jti denylist failed", slog.String("err", err.Error()))
			return nil, status.Error(codes.Internal, "revoke failed")
		}
	}
	if err := s.repo.StorefrontAccount().RevokeAllRefreshTokensForAccount(ctx, acc.ID); err != nil {
		slog.Default().ErrorContext(ctx, "revoke refresh tokens failed", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "revoke failed")
	}
	return &pb_frontend.RevokeAccountSessionResponse{}, nil
}

// GetAccount returns the current profile.
func (s *Server) GetAccount(ctx context.Context, _ *pb_frontend.GetAccountRequest) (*pb_frontend.GetAccountResponse, error) {
	email, err := s.storefrontEmailFromAccess(ctx)
	if err != nil {
		return nil, err
	}
	acc, err := s.repo.StorefrontAccount().GetAccountByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "account not found")
		}
		return nil, status.Error(codes.Internal, "can't load account")
	}
	addressList, err := s.repo.StorefrontAccount().ListSavedAddresses(ctx, acc.ID)
	if err != nil {
		return nil, status.Error(codes.Internal, "can't load addresses")
	}
	pbAddresses := make([]*pb_frontend.StorefrontSavedAddress, 0, len(addressList))
	for i := range addressList {
		pbAddresses = append(pbAddresses, dto.EntityStorefrontSavedAddressToPb(&addressList[i]))
	}
	pbAcc, err := dto.EntityStorefrontAccountToPb(acc, pbAddresses)
	if err != nil {
		return nil, status.Error(codes.Internal, "can't build account")
	}
	return &pb_frontend.GetAccountResponse{Account: pbAcc}, nil
}

// UpdateAccount updates profile fields.
func (s *Server) UpdateAccount(ctx context.Context, req *pb_frontend.UpdateAccountRequest) (*pb_frontend.UpdateAccountResponse, error) {
	email, err := s.storefrontEmailFromAccess(ctx)
	if err != nil {
		return nil, err
	}
	acc, err := s.repo.StorefrontAccount().GetAccountByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "account not found")
		}
		return nil, status.Error(codes.Internal, "can't load account")
	}
	fn := acc.FirstName
	ln := acc.LastName
	if req.FirstName != nil {
		fn = strings.TrimSpace(*req.FirstName)
	}
	if req.LastName != nil {
		ln = strings.TrimSpace(*req.LastName)
	}
	bd := acc.BirthDate
	if req.BirthDate != nil {
		bd = dto.PbDateToNullTime(req.BirthDate)
	}
	shoppingPref := acc.ShoppingPreference
	if req.ShoppingPreference != nil {
		g := *req.ShoppingPreference
		if g == pb_frontend.ShoppingPreferenceEnum_SHOPPING_PREFERENCE_ENUM_UNKNOWN {
			return nil, status.Error(codes.InvalidArgument, "shopping preference cannot be set to unknown")
		}
		sp, err := dto.ConvertPbShoppingPreferenceEnumToEntity(g)
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, "invalid shopping preference")
		}
		shoppingPref = sp
	}
	phone := acc.Phone
	if req.Phone != nil {
		phoneVal := strings.TrimSpace(*req.Phone)
		if phoneVal == "" {
			phone = sql.NullString{}
		} else {
			phone = sql.NullString{String: phoneVal, Valid: true}
		}
	}
	subscribeNewsletter := acc.SubscribeNewsletter
	if req.SubscribeNewsletter != nil {
		subscribeNewsletter = *req.SubscribeNewsletter
	}
	subscribeNewArrivals := acc.SubscribeNewArrivals
	if req.SubscribeNewArrivals != nil {
		subscribeNewArrivals = *req.SubscribeNewArrivals
	}
	subscribeEvents := acc.SubscribeEvents
	if req.SubscribeEvents != nil {
		subscribeEvents = *req.SubscribeEvents
	}
	defaultCountry := acc.DefaultCountry
	if req.DefaultCountry != nil {
		countryVal := strings.TrimSpace(*req.DefaultCountry)
		if countryVal == "" {
			defaultCountry = sql.NullString{}
		} else {
			defaultCountry = sql.NullString{String: countryVal, Valid: true}
		}
	}
	defaultLanguage := acc.DefaultLanguage
	if req.DefaultLanguage != nil {
		langVal := strings.TrimSpace(*req.DefaultLanguage)
		if langVal == "" {
			defaultLanguage = sql.NullString{}
		} else {
			defaultLanguage = sql.NullString{String: langVal, Valid: true}
		}
	}
	if err := s.repo.StorefrontAccount().UpdateAccountProfile(ctx, email, fn, ln, bd, shoppingPref, phone, subscribeNewsletter, subscribeNewArrivals, subscribeEvents, defaultCountry, defaultLanguage); err != nil {
		return nil, status.Error(codes.Internal, "can't update account")
	}
	if req.SubscribeNewsletter != nil {
		if _, err := s.repo.Subscribers().UpsertSubscription(ctx, email, subscribeNewsletter); err != nil {
			slog.Default().ErrorContext(ctx, "can't sync newsletter subscription to subscriber table", slog.String("err", err.Error()))
		}
	}
	acc2, err := s.repo.StorefrontAccount().GetAccountByEmail(ctx, email)
	if err != nil {
		return nil, status.Error(codes.Internal, "can't load account")
	}
	addressList, err := s.repo.StorefrontAccount().ListSavedAddresses(ctx, acc2.ID)
	if err != nil {
		return nil, status.Error(codes.Internal, "can't load addresses")
	}
	pbAddresses := make([]*pb_frontend.StorefrontSavedAddress, 0, len(addressList))
	for i := range addressList {
		pbAddresses = append(pbAddresses, dto.EntityStorefrontSavedAddressToPb(&addressList[i]))
	}
	pbAcc, err := dto.EntityStorefrontAccountToPb(acc2, pbAddresses)
	if err != nil {
		return nil, status.Error(codes.Internal, "can't build account")
	}
	return &pb_frontend.UpdateAccountResponse{Account: pbAcc}, nil
}

func (s *Server) accountID(ctx context.Context) (int, error) {
	email, err := s.storefrontEmailFromAccess(ctx)
	if err != nil {
		return 0, err
	}
	acc, err := s.repo.StorefrontAccount().GetAccountByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, status.Error(codes.NotFound, "account not found")
		}
		return 0, status.Error(codes.Internal, "can't load account")
	}
	return acc.ID, nil
}

// ListSavedAddresses lists saved addresses.
func (s *Server) ListSavedAddresses(ctx context.Context, _ *pb_frontend.ListSavedAddressesRequest) (*pb_frontend.ListSavedAddressesResponse, error) {
	aid, err := s.accountID(ctx)
	if err != nil {
		return nil, err
	}
	list, err := s.repo.StorefrontAccount().ListSavedAddresses(ctx, aid)
	if err != nil {
		return nil, status.Error(codes.Internal, "can't list addresses")
	}
	out := make([]*pb_frontend.StorefrontSavedAddress, 0, len(list))
	for i := range list {
		out = append(out, dto.EntityStorefrontSavedAddressToPb(&list[i]))
	}
	return &pb_frontend.ListSavedAddressesResponse{Addresses: out}, nil
}

// AddSavedAddress adds a saved address.
func (s *Server) AddSavedAddress(ctx context.Context, req *pb_frontend.AddSavedAddressRequest) (*pb_frontend.AddSavedAddressResponse, error) {
	aid, err := s.accountID(ctx)
	if err != nil {
		return nil, err
	}
	list, err := s.repo.StorefrontAccount().ListSavedAddresses(ctx, aid)
	if err != nil {
		return nil, status.Error(codes.Internal, "can't list addresses")
	}
	if len(list) >= maxSavedAddresses {
		return nil, status.Error(codes.ResourceExhausted, "maximum saved addresses reached")
	}
	ins := dto.PbStorefrontSavedAddressToInsert(req.GetAddress())
	if ins == nil || ins.Country == "" || ins.City == "" || ins.AddressLineOne == "" || ins.PostalCode == "" {
		return nil, status.Error(codes.InvalidArgument, "address fields are required")
	}
	id, err := s.repo.StorefrontAccount().AddSavedAddress(ctx, aid, ins)
	if err != nil {
		return nil, status.Error(codes.Internal, "can't add address")
	}
	return &pb_frontend.AddSavedAddressResponse{Id: int32(id)}, nil
}

// UpdateSavedAddress updates a saved address.
func (s *Server) UpdateSavedAddress(ctx context.Context, req *pb_frontend.UpdateSavedAddressRequest) (*pb_frontend.UpdateSavedAddressResponse, error) {
	aid, err := s.accountID(ctx)
	if err != nil {
		return nil, err
	}
	ins := dto.PbStorefrontSavedAddressToInsert(req.GetAddress())
	if ins == nil || req.GetId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "id and address are required")
	}
	if err := s.repo.StorefrontAccount().UpdateSavedAddress(ctx, aid, int(req.GetId()), ins); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "address not found")
		}
		return nil, status.Error(codes.Internal, "can't update address")
	}
	return &pb_frontend.UpdateSavedAddressResponse{}, nil
}

// DeleteSavedAddress removes a saved address.
func (s *Server) DeleteSavedAddress(ctx context.Context, req *pb_frontend.DeleteSavedAddressRequest) (*pb_frontend.DeleteSavedAddressResponse, error) {
	aid, err := s.accountID(ctx)
	if err != nil {
		return nil, err
	}
	if req.GetId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}
	if err := s.repo.StorefrontAccount().DeleteSavedAddress(ctx, aid, int(req.GetId())); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "address not found")
		}
		return nil, status.Error(codes.Internal, "can't delete address")
	}
	return &pb_frontend.DeleteSavedAddressResponse{}, nil
}

// SetDefaultSavedAddress sets the default shipping address.
func (s *Server) SetDefaultSavedAddress(ctx context.Context, req *pb_frontend.SetDefaultSavedAddressRequest) (*pb_frontend.SetDefaultSavedAddressResponse, error) {
	aid, err := s.accountID(ctx)
	if err != nil {
		return nil, err
	}
	if req.GetId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}
	if err := s.repo.StorefrontAccount().SetDefaultSavedAddress(ctx, aid, int(req.GetId())); err != nil {
		return nil, status.Error(codes.Internal, "can't set default address")
	}
	return &pb_frontend.SetDefaultSavedAddressResponse{}, nil
}

// ListMyOrders returns order history for the logged-in email.
func (s *Server) ListMyOrders(ctx context.Context, req *pb_frontend.ListMyOrdersRequest) (*pb_frontend.ListMyOrdersResponse, error) {
	email, err := s.storefrontEmailFromAccess(ctx)
	if err != nil {
		return nil, err
	}
	limit := int(req.GetLimit())
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	offset := int(req.GetOffset())
	if offset < 0 {
		offset = 0
	}
	orders, total, err := s.repo.Order().ListOrdersFullByBuyerEmailPaged(ctx, email, limit, offset)
	if err != nil {
		slog.Default().ErrorContext(ctx, "list my orders", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't list orders")
	}
	out := make([]*pb_common.OrderFull, 0, len(orders))
	for i := range orders {
		pbO, err := dto.ConvertEntityOrderFullToPbOrderFull(&orders[i])
		if err != nil {
			return nil, status.Error(codes.Internal, "can't convert order")
		}
		out = append(out, pbO)
	}
	return &pb_frontend.ListMyOrdersResponse{Orders: out, Total: int32(total)}, nil
}
