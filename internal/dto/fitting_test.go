package dto

import (
	"errors"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestConvertPbFittingInsertToEntity(t *testing.T) {
	date := timestamppb.New(time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC))
	valid := &pb_common.FittingInsert{
		ProductId:   42,
		ModelId:     7,
		FittingDate: date,
		Comment:     "sample run",
		Status:      pb_common.FittingStatus_FITTING_STATUS_DONE,
		Verdict:     pb_common.FittingVerdict_FITTING_VERDICT_APPROVED,
		RecordedBy:  "admin@x.cc",
		Sizes: []*pb_common.FittingSizeInsert{
			{SizeId: 4, FitNote: "perfect"},
			{SizeId: 5, FitNote: ""},
		},
		MediaIds: []int32{11, 12},
	}

	got, err := ConvertPbFittingInsertToEntity(valid)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.ProductId.Valid || got.ProductId.Int32 != 42 || !got.ModelId.Valid || got.ModelId.Int32 != 7 {
		t.Errorf("product/model mismatch: %+v", got)
	}
	if got.Status != entity.FittingDone || got.Verdict != entity.FittingApproved {
		t.Errorf("status/verdict mismatch: %v %v", got.Status, got.Verdict)
	}
	if len(got.Sizes) != 2 || got.Sizes[0].SizeId != 4 || !got.Sizes[0].FitNote.Valid || got.Sizes[1].FitNote.Valid {
		t.Errorf("sizes mismatch: %+v", got.Sizes)
	}
	if len(got.MediaIds) != 2 || got.MediaIds[0] != 11 {
		t.Errorf("media ids mismatch: %+v", got.MediaIds)
	}

	// defaults: unset status/verdict become planned/pending
	def, err := ConvertPbFittingInsertToEntity(&pb_common.FittingInsert{ProductId: 1, FittingDate: date})
	if err != nil {
		t.Fatalf("defaults: %v", err)
	}
	if def.Status != entity.FittingPlanned || def.Verdict != entity.FittingPending {
		t.Errorf("defaults mismatch: %v %v", def.Status, def.Verdict)
	}
	if def.ModelId.Valid {
		t.Errorf("model id 0 should be NULL, got %+v", def.ModelId)
	}

	// a tech-card-only fitting (no product yet, e.g. a proto) is valid.
	tc, err := ConvertPbFittingInsertToEntity(&pb_common.FittingInsert{TechCardId: 8, FittingDate: date})
	if err != nil {
		t.Fatalf("tech-card-only: %v", err)
	}
	if !tc.TechCardId.Valid || tc.TechCardId.Int32 != 8 || tc.ProductId.Valid {
		t.Errorf("tech-card-only anchor mismatch: tc=%+v prod=%+v", tc.TechCardId, tc.ProductId)
	}

	// fitting_date is normalized to the UTC calendar date regardless of time-of-day.
	noon := timestamppb.New(time.Date(2026, 6, 19, 15, 30, 0, 0, time.UTC))
	norm, err := ConvertPbFittingInsertToEntity(&pb_common.FittingInsert{ProductId: 1, FittingDate: noon})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if want := time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC); !norm.FittingDate.Equal(want) {
		t.Errorf("fitting_date not normalized: got %v want %v", norm.FittingDate, want)
	}

	// invalid cases (incl. out-of-range enum values that must be rejected, not defaulted)
	bad := map[string]*pb_common.FittingInsert{
		"nil":            nil,
		"no anchor":      {ProductId: 0, FittingDate: date},
		"no date":        {ProductId: 1},
		"size id zero":   {ProductId: 1, FittingDate: date, Sizes: []*pb_common.FittingSizeInsert{{SizeId: 0}}},
		"duplicate size": {ProductId: 1, FittingDate: date, Sizes: []*pb_common.FittingSizeInsert{{SizeId: 4}, {SizeId: 4}}},
		"bad status":     {ProductId: 1, FittingDate: date, Status: pb_common.FittingStatus(99)},
		"bad verdict":    {ProductId: 1, FittingDate: date, Verdict: pb_common.FittingVerdict(99)},
	}
	for name, in := range bad {
		if _, err := ConvertPbFittingInsertToEntity(in); err == nil {
			t.Errorf("case %q: expected error, got nil", name)
		}
	}
}

func TestConvertEntityFittingToPb(t *testing.T) {
	f := &entity.Fitting{
		Id: 3,
		FittingInsert: entity.FittingInsert{
			TechCardId:  nullInt32FromPb(8),
			ProductId:   nullInt32FromPb(42),
			FittingDate: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
			Status:      entity.FittingPlanned,
			Verdict:     entity.FittingPending,
			Sizes:       []entity.FittingSize{{SizeId: 4}},
		},
		Media: []entity.MediaFull{{Id: 11}, {Id: 12}},
	}
	pb := ConvertEntityFittingToPb(f)
	if pb.Id != 3 || pb.Fitting.ProductId != 42 || pb.Fitting.TechCardId != 8 {
		t.Errorf("id/product/tech_card mismatch: %+v", pb)
	}
	if pb.Fitting.Status != pb_common.FittingStatus_FITTING_STATUS_PLANNED ||
		pb.Fitting.Verdict != pb_common.FittingVerdict_FITTING_VERDICT_PENDING {
		t.Errorf("status/verdict mismatch: %v %v", pb.Fitting.Status, pb.Fitting.Verdict)
	}
	if len(pb.Media) != 2 || len(pb.Fitting.MediaIds) != 2 || pb.Fitting.MediaIds[1] != 12 {
		t.Errorf("media mismatch: media=%d ids=%+v", len(pb.Media), pb.Fitting.MediaIds)
	}
}

// TestFittingCalloutsRoundTrip covers parsing, validating and re-emitting fitting photo
// callouts (pin + note), plus the validation rejections.
func TestFittingCalloutsRoundTrip(t *testing.T) {
	date := timestamppb.New(time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC))
	in := &pb_common.FittingInsert{
		ProductId:   42,
		FittingDate: date,
		Callouts: []*pb_common.FittingCallout{
			{Number: 1, Note: "shoulder too tight", MediaId: 11, PosX: &pb_decimal.Decimal{Value: "0.25"}, PosY: &pb_decimal.Decimal{Value: "0.5"}},
			{Number: 2, Note: "hem uneven"}, // unanchored, no position
		},
	}
	got, err := ConvertPbFittingInsertToEntity(in)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if len(got.Callouts) != 2 {
		t.Fatalf("callouts not parsed: %+v", got.Callouts)
	}
	if got.Callouts[0].Number != 1 || got.Callouts[0].Note.String != "shoulder too tight" ||
		got.Callouts[0].MediaId.Int32 != 11 || !got.Callouts[0].PosX.Valid || got.Callouts[0].PosX.Decimal.String() != "0.25" {
		t.Errorf("callout[0] mismatch: %+v", got.Callouts[0])
	}
	if got.Callouts[1].MediaId.Valid || got.Callouts[1].PosX.Valid {
		t.Errorf("callout[1] should be unanchored with no position: %+v", got.Callouts[1])
	}

	pb := ConvertEntityFittingToPb(&entity.Fitting{FittingInsert: *got})
	if len(pb.Fitting.Callouts) != 2 || pb.Fitting.Callouts[0].Note != "shoulder too tight" ||
		pb.Fitting.Callouts[0].MediaId != 11 || pb.Fitting.Callouts[0].PosX == nil || pb.Fitting.Callouts[0].PosX.Value != "0.25" ||
		pb.Fitting.Callouts[0].PosY == nil || pb.Fitting.Callouts[0].PosY.Value != "0.5" {
		t.Errorf("anchored callout round-trip mismatch: %+v", pb.Fitting.Callouts)
	}
	// the unanchored callout emits with no media/position.
	if pb.Fitting.Callouts[1].Note != "hem uneven" || pb.Fitting.Callouts[1].MediaId != 0 ||
		pb.Fitting.Callouts[1].PosX != nil || pb.Fitting.Callouts[1].PosY != nil {
		t.Errorf("unanchored callout round-trip mismatch: %+v", pb.Fitting.Callouts[1])
	}

	pin := pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_PIN
	bad := map[string]*pb_common.FittingInsert{
		"note required":   {ProductId: 1, FittingDate: date, Callouts: []*pb_common.FittingCallout{{Number: 1, Note: "  ", Kind: &pin}}},
		"note too long":   {ProductId: 1, FittingDate: date, Callouts: []*pb_common.FittingCallout{{Number: 1, Note: string(make([]byte, maxTaskText+1))}}},
		"negative number": {ProductId: 1, FittingDate: date, Callouts: []*pb_common.FittingCallout{{Number: -1, Note: "x"}}},
		"negative media":  {ProductId: 1, FittingDate: date, Callouts: []*pb_common.FittingCallout{{Note: "x", MediaId: -1}}},
		"pos_x over 1":    {ProductId: 1, FittingDate: date, Callouts: []*pb_common.FittingCallout{{Note: "x", PosX: &pb_decimal.Decimal{Value: "1.5"}}}},
	}
	for name, bi := range bad {
		if _, err := ConvertPbFittingInsertToEntity(bi); err == nil {
			t.Errorf("case %q: expected error, got nil", name)
		}
	}
}

// TestFittingCalloutNoteRequiredOnlyForPin: записка — содержание ПИНА, но не фигуры.
//
// Пока выноска была нумерованной точкой, требование записки у каждой было верным: точка без слов
// не сообщает ничего. С появлением видов (0319) оно стало требованием подписи к предложению —
// обведённая зона заломов уже сказала, что не так. Клиенту при таком сервере остаётся выбрасывать
// безымянные фигуры перед отправкой, то есть терять нарисованное руками молча.
func TestFittingCalloutNoteRequiredOnlyForPin(t *testing.T) {
	date := timestamppb.New(time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC))
	kind := func(k pb_common.TechCardAnnotationKind) *pb_common.TechCardAnnotationKind { return &k }
	pt := func(x, y string) *pb_common.TechCardAnnotationPoint {
		return &pb_common.TechCardAnnotationPoint{
			X: &pb_decimal.Decimal{Value: x}, Y: &pb_decimal.Decimal{Value: y},
		}
	}

	// Фигуры без единого слова — законны, и содержание у них своё.
	ok := []*pb_common.FittingCallout{
		{Number: 1, Kind: kind(pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_POLYGON),
			Points: []*pb_common.TechCardAnnotationPoint{pt("0.1", "0.1"), pt("0.4", "0.1"), pt("0.4", "0.4")}, Filled: true},
		{Number: 2, Kind: kind(pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_ARC),
			Points: []*pb_common.TechCardAnnotationPoint{pt("0.3", "0.2"), pt("0.4", "0.3"), pt("0.5", "0.2")}},
		{Number: 3, Kind: kind(pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_DIM),
			Points: []*pb_common.TechCardAnnotationPoint{pt("0.2", "0.5"), pt("0.6", "0.5")}, Note: "   "},
	}
	got, err := ConvertPbFittingInsertToEntity(&pb_common.FittingInsert{
		ProductId: 1, FittingDate: date, Callouts: ok,
	})
	if err != nil {
		t.Fatalf("фигура без текста обязана сохраняться: %v", err)
	}
	if len(got.Callouts) != 3 {
		t.Fatalf("callouts not parsed: %+v", got.Callouts)
	}
	for i, c := range got.Callouts {
		if c.Note.Valid {
			t.Errorf("callout[%d]: пустая записка обязана лечь NULL, а не пустой строкой: %+v", i, c.Note)
		}
	}

	// А ПИН без слов — по-прежнему пустое место, и отказ обязан назвать строку.
	_, err = ConvertPbFittingInsertToEntity(&pb_common.FittingInsert{
		ProductId: 1, FittingDate: date,
		Callouts: []*pb_common.FittingCallout{
			{Number: 1, Note: "плечо жмёт", Kind: kind(pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_PIN)},
			{Number: 2, Kind: kind(pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_PIN)},
		},
	})
	if err == nil {
		t.Fatal("пин без записки не сообщает ничего: отказ обязателен")
	}
	var ve *entity.ValidationError
	if !errors.As(err, &ve) || ve.Field != "callouts[1].note" {
		t.Errorf("отказ обязан назвать выноску (callouts[1].note), получено: %v", err)
	}

	// ВКЛАДКА СО СТАРЫМ БАНДЛОМ про вид молчит, а её выноска на сервере запросто зона: требовать с
	// неё записку значило бы отвергнуть всю примерку за фигуру, которую сервер сейчас же и вернёт
	// переносом.
	if _, err := ConvertPbFittingInsertToEntity(&pb_common.FittingInsert{
		ProductId: 1, FittingDate: date,
		Callouts: []*pb_common.FittingCallout{{Number: 1}},
	}); err != nil {
		t.Errorf("молчание про вид — не заявление о содержании: %v", err)
	}
}
