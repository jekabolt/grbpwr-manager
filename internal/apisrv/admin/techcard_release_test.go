package admin

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestSnapshotReleaseBuildsStoreNumberedMetadata(t *testing.T) {
	now := time.Date(2026, time.August, 1, 12, 0, 0, 0, time.UTC)
	repo := mocks.NewMockRepository(t)
	tc := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(tc)
	tc.EXPECT().GetTechCardByIdConsistent(mock.Anything, 5).Return(&entity.TechCard{
		Id: 5,
		TechCardInsert: entity.TechCardInsert{
			Name:          "Release Coat",
			ApprovalState: entity.TechCardApprovalReleased,
			ReleasedAt:    sql.NullTime{Time: now, Valid: true},
		},
		CreatedAt: now,
		UpdatedAt: now,
	}, nil)
	tc.EXPECT().GetCostingFxRatesToBase(mock.Anything).Return(nil, nil)
	tc.EXPECT().SaveTechCardRelease(mock.Anything, mock.AnythingOfType("entity.TechCardRelease")).
		Run(func(_ context.Context, rel entity.TechCardRelease) {
			require.Equal(t, 5, rel.TechCardId)
			require.Zero(t, rel.ReleaseNumber, "the store assigns release_number atomically")
		}).Return(nil)

	(&Server{repo: repo}).snapshotReleaseIfReleased(context.Background(), 5)
}

// GetTechCardRelease ignores retired fields in old proto-JSON snapshots so removing a dead wire field
// does not make an otherwise-readable frozen release degrade to snapshot_error.
func TestGetTechCardReleaseParsesSnapshotWithRetiredFields(t *testing.T) {
	blob := `{"id":5,"techCard":{"name":"Release Coat"},"revisions":[{"version":"1.0","revisionDate":"2025-01-02T00:00:00Z","author":"alice"}]}`

	repo := mocks.NewMockRepository(t)
	tc := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(tc)
	tc.EXPECT().GetTechCardRelease(mock.Anything, 9).Return(&entity.TechCardRelease{
		TechCardReleaseMeta: entity.TechCardReleaseMeta{Id: 9, TechCardId: 5, ReleaseNumber: 1},
		Snapshot:            blob,
	}, nil)

	s := &Server{repo: repo}
	resp, err := s.GetTechCardRelease(context.Background(), &pb_admin.GetTechCardReleaseRequest{Id: 9})
	require.NoError(t, err)
	require.Empty(t, resp.SnapshotError)
	require.NotNil(t, resp.Snapshot)
	require.Equal(t, int32(5), resp.Snapshot.Id)
	require.NotNil(t, resp.Snapshot.TechCard)
	require.Equal(t, "Release Coat", resp.Snapshot.TechCard.Name)
	require.Equal(t, int32(1), resp.Release.ReleaseNumber)
}

// A stored blob that no longer parses must degrade to metadata + snapshot_error, never a 500
// (hero-v2 rule) — old releases stay listable/openable as the contract evolves.
func TestGetTechCardReleaseDegradesOnBadBlob(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	tc := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(tc)
	tc.EXPECT().GetTechCardRelease(mock.Anything, 3).Return(&entity.TechCardRelease{
		TechCardReleaseMeta: entity.TechCardReleaseMeta{Id: 3, TechCardId: 5},
		Snapshot:            `{"this":"is valid json but not a TechCard","id":"not-an-int"}`,
	}, nil)

	s := &Server{repo: repo}
	resp, err := s.GetTechCardRelease(context.Background(), &pb_admin.GetTechCardReleaseRequest{Id: 3})
	require.NoError(t, err, "a bad blob must not fail the call")
	require.Nil(t, resp.Snapshot)
	require.NotEmpty(t, resp.SnapshotError)
	require.Equal(t, int32(3), resp.Release.Id)
}

// readSnapshotBlob feeds a stored release blob through the real read path, so every assertion below
// is about what an operator opening an archived release actually sees.
func readSnapshotBlob(t *testing.T, blob string) *pb_admin.GetTechCardReleaseResponse {
	t.Helper()
	repo := mocks.NewMockRepository(t)
	tc := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(tc)
	tc.EXPECT().GetTechCardRelease(mock.Anything, 9).Return(&entity.TechCardRelease{
		TechCardReleaseMeta: entity.TechCardReleaseMeta{Id: 9, TechCardId: 5, ReleaseNumber: 1},
		Snapshot:            blob,
	}, nil)
	resp, err := (&Server{repo: repo}).GetTechCardRelease(fullAccessCtx(), &pb_admin.GetTechCardReleaseRequest{Id: 9})
	require.NoError(t, err)
	return resp
}

func snapshotWithOperationType(enumName string) string {
	return fmt.Sprintf(`{"id":5,"techCard":{"name":"Archived Coat","operations":[`+
		`{"operationNumber":10,"operationType":%q,"zone":"TECH_CARD_GARMENT_ZONE_OUTER","note":"притачать"}]}}`, enumName)
}

// Архивный релиз хранится как protojson контракта и читается СЕГОДНЯШНИМ протоколом. Поэтому девять
// legacy-значений типа операции живут в словаре вечно (§1.4 плана) — и этот тест фиксирует именно
// решение, а не парсер: старое значение обязано после разбора остаться САМИМ СОБОЙ и по-прежнему
// называть свою машинку.
//
// Цена обратного решения — в подтесте внизу: снапшот разбирается с DiscardUnknown, а DiscardUnknown
// гасит неизвестное ИМЯ enum'а в ноль молча, без ошибки разбора. Удалив значение, мы бы превратили
// каждый архивный `overlock` в «unknown» — и релиз, который по определению нельзя переподписать,
// начал бы врать про то, на чём его шили.
func TestGetTechCardReleaseKeepsArchivedLegacyOperationTypesMeaningful(t *testing.T) {
	// Девять и ровно девять: список закрыт навсегда. Пин стоит здесь затем, чтобы «уборка» одного
	// из значений не сузила покрытие этого теста молча — цикл ниже ходит по этой же карте.
	require.Len(t, entity.LegacyOperationMachineType, 9)

	seen := make(map[pb_common.TechCardOperationType]string, len(entity.LegacyOperationMachineType))
	for legacy, machine := range entity.LegacyOperationMachineType {
		name := "TECH_CARD_OPERATION_TYPE_" + strings.ToUpper(string(legacy))
		resp := readSnapshotBlob(t, snapshotWithOperationType(name))

		require.Empty(t, resp.SnapshotError, "an archived release must stay readable: %s", name)
		require.NotNil(t, resp.Snapshot)
		require.NotNil(t, resp.Snapshot.TechCard)
		require.Len(t, resp.Snapshot.TechCard.Operations, 1)
		got := resp.Snapshot.TechCard.Operations[0].OperationType

		require.NotEqual(t, pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_UNKNOWN, got,
			"%s survived the parse as «unknown» — the archived step lost what it was", name)
		require.Equal(t, name, got.String(), "the archived value must parse back as itself")
		require.NotContains(t, seen, got,
			"%s and %s collapsed onto one value — two different archived steps would render the same", name, seen[got])
		seen[got] = name

		// И оно по-прежнему НАЗЫВАЕТ машинку: разобранное имя проходит через ту же единственную
		// карту канонизации, по которой живут dto, дайджест и миграция 0306. Это и есть «смысл»:
		// клиент, рисующий архивный шаг, может сказать «оверлок», а не «неизвестно».
		token := entity.TechCardOperationType(strings.ToLower(strings.TrimPrefix(got.String(), "TECH_CARD_OPERATION_TYPE_")))
		require.Equal(t, machine, entity.LegacyOperationMachineType[token],
			"the archived %s must still resolve to the machine it named", name)
	}

	t.Run("a name the contract no longer knows degrades to UNKNOWN without a word", func(t *testing.T) {
		resp := readSnapshotBlob(t, snapshotWithOperationType("TECH_CARD_OPERATION_TYPE_LOCKSTITCH_RETIRED"))
		require.Empty(t, resp.SnapshotError, "DiscardUnknown does not even report the loss")
		require.Len(t, resp.Snapshot.TechCard.Operations, 1)
		require.Equal(t, pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_UNKNOWN,
			resp.Snapshot.TechCard.Operations[0].OperationType,
			"this is exactly what retiring a legacy value would do to every release already frozen")
	})
}

// Второй конец той же нити: снапшот карточки НОВОЙ формы обязан унести с собой парк оборудования и
// машинные поля шагов — сквозь entity → pb → protojson → pb и обратно в ответ на чтение релиза.
//
// Тут ловится ровно то, что не ловится ни dto-тестом (там обёртку собирает рука теста), ни
// стор-тестом (там нет protojson): снапшот пишется тем же эмиттером, что и обычное чтение, и
// незаполненная обёртка сделала бы каждый замороженный релиз карточкой без единой машинки.
func TestReleaseSnapshotCarriesEquipmentParkAndMachineFields(t *testing.T) {
	const overlockKey = "OVERLOCKPROFILEKEY00000001"
	const fusingKey = "FUSINGPRESSPROFILEKEY00001"
	require.Len(t, overlockKey, 26)
	require.Len(t, fusingKey, 26)

	now := time.Date(2026, time.August, 1, 12, 0, 0, 0, time.UTC)
	card := &entity.TechCard{
		Id: 5,
		TechCardInsert: entity.TechCardInsert{
			Name:          "Release Coat",
			ApprovalState: entity.TechCardApprovalReleased,
			ReleasedAt:    sql.NullTime{Time: now, Valid: true},
			Construction: &entity.TechCardConstruction{
				EquipmentDefaults: &entity.TechCardEquipmentDefaults{
					Machines: []entity.TechCardMachineProfile{{
						ProfileKey:    overlockKey,
						Label:         sql.NullString{String: "оверлок у окна", Valid: true},
						MachineType:   "overlock",
						ThreadCount:   sql.NullInt32{Int32: 4, Valid: true},
						NeedleType:    sql.NullString{String: "ballpoint", Valid: true},
						StitchesPerCm: decimal.NullDecimal{Decimal: decimal.RequireFromString("4"), Valid: true},
					}},
					Presses: []entity.TechCardPressProfile{{
						ProfileKey:         fusingKey,
						Label:              sql.NullString{String: "дублирующий пресс", Valid: true},
						PressEquipment:     "fusing_press",
						PressOperationType: sql.NullString{String: string(entity.OpTypeFusing), Valid: true},
						PressTemperatureC:  sql.NullInt32{Int32: 145, Valid: true},
						PressDwellSec:      sql.NullInt32{Int32: 12, Valid: true},
						PressSteam:         sql.NullBool{Bool: false, Valid: true},
					}},
				},
			},
			Operations: []entity.TechCardOperation{
				{
					OperationNumber:   sql.NullInt32{Int32: 10, Valid: true},
					OperationType:     entity.OpTypeMachine,
					MachineType:       sql.NullString{String: "overlock", Valid: true},
					MachineProfileKey: sql.NullString{String: overlockKey, Valid: true},
					ThreadCount:       sql.NullInt32{Int32: 3, Valid: true},
					NeedleSizeNm:      sql.NullInt32{Int32: 90, Valid: true},
					StitchWidthMm:     decimal.NullDecimal{Decimal: decimal.RequireFromString("5"), Valid: true},
					Zone:              entity.ZoneOuter,
				},
				{
					OperationNumber:   sql.NullInt32{Int32: 20, Valid: true},
					OperationType:     entity.OpTypeFusing,
					PressEquipment:    sql.NullString{String: "fusing_press", Valid: true},
					PressProfileKey:   sql.NullString{String: fusingKey, Valid: true},
					PressTemperatureC: sql.NullInt32{Int32: 150, Valid: true},
					PressSteam:        sql.NullBool{Bool: false, Valid: true},
					PressCloth:        sql.NullString{String: "none", Valid: true},
					Zone:              entity.ZoneInterlining,
				},
			},
		},
		CreatedAt: now,
		UpdatedAt: now,
	}

	repo := mocks.NewMockRepository(t)
	tc := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(tc)
	tc.EXPECT().GetTechCardByIdConsistent(mock.Anything, 5).Return(card, nil)
	tc.EXPECT().GetCostingFxRatesToBase(mock.Anything).Return(nil, nil)
	var blob string
	tc.EXPECT().SaveTechCardRelease(mock.Anything, mock.AnythingOfType("entity.TechCardRelease")).
		Run(func(_ context.Context, rel entity.TechCardRelease) { blob = rel.Snapshot }).Return(nil)

	(&Server{repo: repo}).snapshotReleaseIfReleased(fullAccessCtx(), 5)
	require.NotEmpty(t, blob)

	resp := readSnapshotBlob(t, blob)
	require.Empty(t, resp.SnapshotError)
	require.NotNil(t, resp.Snapshot.TechCard.Construction)
	park := resp.Snapshot.TechCard.Construction.EquipmentDefaults
	require.NotNil(t, park, "a frozen release without the wrapper is a card whose park was never captured")

	require.Len(t, park.Machines, 1)
	m := park.Machines[0]
	require.Equal(t, overlockKey, m.ProfileKey)
	require.Equal(t, "оверлок у окна", m.Label)
	require.Equal(t, pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_OVERLOCK, m.MachineType)
	require.Equal(t, int32(4), m.ThreadCount)
	require.Equal(t, pb_common.TechCardNeedleType_TECH_CARD_NEEDLE_TYPE_BALLPOINT, m.NeedleType)
	require.Equal(t, "4", m.StitchesPerCm.GetValue())

	require.Len(t, park.Presses, 1)
	p := park.Presses[0]
	require.Equal(t, fusingKey, p.ProfileKey)
	require.Equal(t, "дублирующий пресс", p.Label)
	require.Equal(t, pb_common.TechCardPressEquipment_TECH_CARD_PRESS_EQUIPMENT_FUSING_PRESS, p.PressEquipment)
	require.Equal(t, pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_FUSING, p.OperationType)
	require.Equal(t, int32(145), p.PressTemperatureC)
	require.Equal(t, int32(12), p.PressDwellSec)
	// «Без пара» переживает и protojson: optional bool сериализуется явным false, а не пропадает
	// как «незаполненный» — иначе архив вернул бы инструкцию в дефолт.
	require.NotNil(t, p.PressSteam, "«без пара» must not come back as «not stated»")
	require.False(t, *p.PressSteam)

	require.Len(t, resp.Snapshot.TechCard.Operations, 2)
	machineStep := resp.Snapshot.TechCard.Operations[0]
	require.Equal(t, pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_MACHINE, machineStep.OperationType)
	require.Equal(t, pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_OVERLOCK, machineStep.MachineType)
	require.Equal(t, overlockKey, machineStep.MachineProfileKey, "the archived step keeps its pointer into the park")
	require.Equal(t, int32(3), machineStep.ThreadCount)
	require.Equal(t, int32(90), machineStep.NeedleSizeNm)
	require.Equal(t, "5", machineStep.StitchWidthMm.GetValue())

	pressStep := resp.Snapshot.TechCard.Operations[1]
	require.Equal(t, pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_FUSING, pressStep.OperationType)
	require.Equal(t, pb_common.TechCardPressEquipment_TECH_CARD_PRESS_EQUIPMENT_FUSING_PRESS, pressStep.PressEquipment)
	require.Equal(t, fusingKey, pressStep.PressProfileKey)
	require.Equal(t, int32(150), pressStep.PressTemperatureC)
	require.NotNil(t, pressStep.PressSteam)
	require.False(t, *pressStep.PressSteam)
	// 'none' — это инструкция «без проутюжильника», а не «не сказано»: NONE обязан доехать до
	// архива членом словаря, а не нулём.
	require.Equal(t, pb_common.TechCardPressCloth_TECH_CARD_PRESS_CLOTH_NONE, pressStep.PressCloth)
}

func TestGetTechCardReleaseNotFoundAndValidation(t *testing.T) {
	// missing id → NotFound
	repo := mocks.NewMockRepository(t)
	tc := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(tc)
	tc.EXPECT().GetTechCardRelease(mock.Anything, 404).Return(nil, sql.ErrNoRows)
	s := &Server{repo: repo}
	_, err := s.GetTechCardRelease(context.Background(), &pb_admin.GetTechCardReleaseRequest{Id: 404})
	require.Equal(t, codes.NotFound, status.Code(err))

	// zero id → InvalidArgument (no store call)
	s2 := &Server{repo: mocks.NewMockRepository(t)}
	_, err = s2.GetTechCardRelease(context.Background(), &pb_admin.GetTechCardReleaseRequest{Id: 0})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

// ListTechCardReleases converts each stored metadata row to pb (newest-first order preserved).
func TestListTechCardReleases(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	tc := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(tc)
	tc.EXPECT().ListTechCardReleases(mock.Anything, 5).Return([]entity.TechCardReleaseMeta{
		{Id: 2, TechCardId: 5, ReleaseNumber: 2},
		{Id: 1, TechCardId: 5, ReleaseNumber: 1},
	}, nil)

	s := &Server{repo: repo}
	resp, err := s.ListTechCardReleases(context.Background(), &pb_admin.ListTechCardReleasesRequest{TechCardId: 5})
	require.NoError(t, err)
	require.Len(t, resp.Releases, 2)
	require.Equal(t, int32(2), resp.Releases[0].ReleaseNumber)
	require.Equal(t, int32(1), resp.Releases[1].ReleaseNumber)

	// zero id → InvalidArgument
	s2 := &Server{repo: mocks.NewMockRepository(t)}
	_, err = s2.ListTechCardReleases(context.Background(), &pb_admin.ListTechCardReleasesRequest{TechCardId: 0})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}
