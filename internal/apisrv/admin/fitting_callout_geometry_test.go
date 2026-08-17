package admin

import (
	"context"
	"database/sql"
	"testing"
	"time"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Контракт присутствия геометрии указаний примерки — НА УРОВНЕ ХЕНДЛЕРА (0319).
//
// Тесты dto проверяют разбор и перенос, но не то, что хендлер их вообще зовёт: гейт «умолчали →
// прочитать хранимое», ветку NotFound на этом чтении и структуру отказа. Сегодняшние мок-тесты
// примерки проходят только потому, что шлют ноль выносок, то есть по новому коду не идут вовсе.

func geomDec(v string) *pb_decimal.Decimal { return &pb_decimal.Decimal{Value: v} }

func geomNullDec(v string) decimal.NullDecimal {
	return decimal.NullDecimal{Decimal: decimal.RequireFromString(v), Valid: true}
}

// storedFittingWithDim — хранимая примерка с ОДНОЙ меркой: две точки, пунктир, красный.
func storedFittingWithDim() *entity.Fitting {
	return &entity.Fitting{
		Id:          1,
		LockVersion: 3,
		FittingInsert: entity.FittingInsert{
			Callouts: []entity.FittingCallout{{
				Number:  1,
				Note:    sql.NullString{String: "по груди уже", Valid: true},
				MediaId: sql.NullInt32{Int32: 42, Valid: true},
				PosX:    geomNullDec("0.2000"), // как её отдаёт колонка DECIMAL(5,4)
				PosY:    geomNullDec("0.3000"),
				Kind:    entity.AnnotationKindDim,
				Color:   entity.AnnotationColorRed,
				Dashed:  true,
				Points: []entity.TechCardAnnotationPoint{
					{X: decimal.RequireFromString("0.2"), Y: decimal.RequireFromString("0.5")},
					{X: decimal.RequireFromString("0.6"), Y: decimal.RequireFromString("0.5")},
				},
			}},
		},
	}
}

// silentCallout — то, что шлёт вкладка со старым бандлом: номер, записка, снимок и маркер, и НИ
// СЛОВА про фигуру. Координаты — те, что она прочитала с сервера.
func silentCallout() *pb_common.FittingCallout {
	return &pb_common.FittingCallout{
		Number: 1, Note: "по груди уже", MediaId: 42,
		PosX: geomDec("0.2"), PosY: geomDec("0.3"),
	}
}

// Гейт действительно читает хранимое, и перенос доезжает ДО стора.
func TestUpdateFittingCarriesOmittedCalloutGeometry(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	f := mocks.NewMockFittings(t)
	repo.EXPECT().Fittings().Return(f)
	f.EXPECT().GetFittingById(mock.Anything, 1).Return(storedFittingWithDim(), nil)

	var written *entity.FittingInsert
	f.EXPECT().UpdateFittingAndListOrphanedPatternURLs(mock.Anything, 1, mock.Anything, 3).
		Run(func(_ context.Context, _ int, fi *entity.FittingInsert, _ int) { written = fi }).
		Return(nil, nil)

	s := &Server{repo: repo}
	_, err := s.UpdateFitting(context.Background(), &pb_admin.UpdateFittingRequest{
		Id:                  1,
		ExpectedLockVersion: 3,
		Fitting: &pb_common.FittingInsert{
			TechCardId: 7, FittingDate: timestamppb.New(time.Now()),
			Callouts: []*pb_common.FittingCallout{silentCallout()},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, written)
	require.Len(t, written.Callouts, 1)
	require.Equal(t, entity.AnnotationKindDim, written.Callouts[0].Kind,
		"без переноса в стор уехал бы пин и мерка была бы стёрта сохранением, которое снимков не открывало")
	require.Len(t, written.Callouts[0].Points, 2)
	require.Equal(t, entity.AnnotationColorRed, written.Callouts[0].Color)
	require.True(t, written.Callouts[0].Dashed)
}

// Новый клиент шлёт вид всегда — и лишнего чтения всей примерки за это не платит. Отсутствие
// ожидания на GetFittingById и есть утверждение: мок падает на незаявленном вызове.
func TestUpdateFittingSkipsStoredReadWhenGeometrySpoken(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	f := mocks.NewMockFittings(t)
	repo.EXPECT().Fittings().Return(f)
	f.EXPECT().UpdateFittingAndListOrphanedPatternURLs(mock.Anything, 1, mock.Anything, 0).Return(nil, nil)

	pin := pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_PIN
	s := &Server{repo: repo}
	_, err := s.UpdateFitting(context.Background(), &pb_admin.UpdateFittingRequest{
		Id: 1,
		Fitting: &pb_common.FittingInsert{
			TechCardId: 7, FittingDate: timestamppb.New(time.Now()),
			Callouts: []*pb_common.FittingCallout{{
				Number: 1, Note: "плечо жмёт", MediaId: 42,
				PosX: geomDec("0.2"), PosY: geomDec("0.3"), Kind: &pin,
			}},
		},
	})
	require.NoError(t, err)
}

// Примерка, исчезнувшая между открытием вкладки и сохранением: чтение ради переноса обязано
// отвечать NotFound, а не пятисоткой.
func TestUpdateFittingStoredReadNotFound(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	f := mocks.NewMockFittings(t)
	repo.EXPECT().Fittings().Return(f)
	f.EXPECT().GetFittingById(mock.Anything, 1).Return(nil, sql.ErrNoRows)

	s := &Server{repo: repo}
	_, err := s.UpdateFitting(context.Background(), &pb_admin.UpdateFittingRequest{
		Id: 1,
		Fitting: &pb_common.FittingInsert{
			TechCardId: 7, FittingDate: timestamppb.New(time.Now()),
			Callouts: []*pb_common.FittingCallout{silentCallout()},
		},
	})
	require.Equal(t, codes.NotFound, status.Code(err))
}

// Структурный отказ доезжает С ИМЕНЕМ ПОЛЯ. Именно ради этого поля и заводились: клиент обязан
// подсветить выноску, из-за которой отказ, а не показать плоскую строку.
func TestFittingCalloutGeometryRefusalNamesTheField(t *testing.T) {
	arc := pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_ARC
	bad := &pb_common.FittingInsert{
		TechCardId: 7, FittingDate: timestamppb.New(time.Now()),
		Callouts: []*pb_common.FittingCallout{{
			Number: 1, Note: "залом", Kind: &arc,
			Points: []*pb_common.TechCardAnnotationPoint{
				{X: geomDec("0.1"), Y: geomDec("0.1")},
				{X: geomDec("0.4"), Y: geomDec("0.4")},
			},
		}},
	}

	// Оба входа, а не только Update: клон сезона и импорт заводят примерку через Add.
	for name, call := range map[string]func(*Server) error{
		"UpdateFitting": func(s *Server) error {
			_, err := s.UpdateFitting(context.Background(), &pb_admin.UpdateFittingRequest{Id: 1, Fitting: bad})
			return err
		},
		"AddFitting": func(s *Server) error {
			_, err := s.AddFitting(context.Background(), &pb_admin.AddFittingRequest{Fitting: bad})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			// Стор не ожидается вовсе: отказ обязан случиться до любой попытки записи.
			s := &Server{repo: mocks.NewMockRepository(t)}
			err := call(s)
			require.Equal(t, codes.InvalidArgument, status.Code(err))

			st, ok := status.FromError(err)
			require.True(t, ok)
			var field string
			for _, d := range st.Details() {
				br, ok := d.(*errdetails.BadRequest)
				if !ok {
					continue
				}
				for _, v := range br.GetFieldViolations() {
					field = v.GetField()
				}
			}
			require.Equal(t, "callouts[0].points", field,
				"плоская строка вместо BadRequest — это мёртвый механизм именованных полей")
		})
	}
}
