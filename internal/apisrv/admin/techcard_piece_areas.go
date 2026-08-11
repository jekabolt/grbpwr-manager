package admin

import (
	"context"
	"log/slog"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/apisrv/apierr"
	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ПЛОЩАДИ ДЕТАЛЕЙ КРОЯ (Ф0, 0297) — the handler half.
//
// The server does not parse DXF and is not going to start (the same statement 0280 makes and for
// the same reason: the heuristic that decides which token in a block name is a size, and the
// geometry pipeline that inflates a seam-line contour by its allowance, live in ONE place — the
// browser). What crosses the wire is the RESULT of that measurement.
//
// What the server does own is the half that could be forged in the dangerous direction: the claim
// «these areas were measured from the card's current patterns». The client names the sheets, the
// STORE fingerprints them from its own rows and refuses a set that does not match. So re-uploading
// any sheet turns the stored areas stale by itself, without anyone having to notice.

// SaveTechCardPieceAreas stores one fabric scope's measured cut-piece areas.
func (s *Server) SaveTechCardPieceAreas(ctx context.Context, req *pb_admin.SaveTechCardPieceAreasRequest) (*pb_admin.SaveTechCardPieceAreasResponse, error) {
	if req.GetTechCardId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "tech_card_id is required")
	}
	if strings.TrimSpace(req.GetScopeKey()) == "" {
		return nil, status.Error(codes.InvalidArgument, "scope_key is required")
	}
	// AN EMPTY SET IS REFUSED HERE, ahead of the store, so the message can say what to do about it.
	// Unlike the size index — where «the files carry no size coding» is a real answer worth
	// recording — a fabric with no measured pieces is never a finding: either the scope has no
	// pieces (and then nothing should have been submitted) or the parse failed. Storing it would
	// turn a failed read into a confident zero area, i.e. a garment that costs no cloth.
	if len(req.GetAreas()) == 0 {
		return nil, status.Error(codes.InvalidArgument,
			"no areas submitted — a parse that found nothing is a failed read; check the contour layer and the block↔piece links")
	}
	// УСЛОВИЯ ЗАМЕРА ОБЯЗАТЕЛЬНЫ, И ОТСУТСТВУЮЩИЙ ПРИПУСК — НЕ НОЛЬ.
	//
	// Слой контура и припуск не украшение: слой шва надо раздуть припуском, слой кроя — уже нет, и
	// ошибка здесь сдвигает норму на величину припуска по всему периметру каждой детали. Приняв
	// пустой слой или подставив нулевой припуск вместо неприсланного, сервер сохранил бы площадь,
	// которую невозможно ни воспроизвести, ни оспорить — она выглядела бы точной.
	//
	// Ноль как ЗНАЧЕНИЕ законен (слой кроя припуска не требует); незаполненность — нет.
	if strings.TrimSpace(req.GetContourLayer()) == "" {
		return nil, status.Error(codes.InvalidArgument,
			"contour_layer is required — an area measured on an unnamed layer cannot be reproduced")
	}
	if req.GetSeamAllowanceMm() == nil || strings.TrimSpace(req.GetSeamAllowanceMm().GetValue()) == "" {
		return nil, status.Error(codes.InvalidArgument,
			"seam_allowance_mm is required — send 0 explicitly for a cutting-line layer; an absent allowance is not a zero one")
	}
	in, err := dto.PieceAreaWriteFromPb(
		int(req.GetTechCardId()),
		req.GetScopeKey(),
		req.GetSheetLineKeys(),
		req.GetAreas(),
		req.GetContourLayer(),
		req.GetSeamAllowanceMm(),
		authsrv.GetAdminUsername(ctx),
	)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	res, err := s.repo.TechCards().SaveTechCardPieceAreas(ctx, in)
	if err != nil {
		if st, ok := apierr.Status(err); ok {
			return nil, st
		}
		if s.repo.IsErrForeignKeyViolation(err) {
			return nil, status.Error(codes.InvalidArgument, "tech_card_id does not exist")
		}
		slog.Default().ErrorContext(ctx, "can't write piece areas", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't write piece areas")
	}
	return &pb_admin.SaveTechCardPieceAreasResponse{
		SheetFingerprint: res.SheetFingerprint,
		Stored:           int32(res.Stored),
	}, nil
}
