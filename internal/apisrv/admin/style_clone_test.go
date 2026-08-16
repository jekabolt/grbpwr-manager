package admin

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func cloneSourceCard() *entity.TechCard {
	now := time.Date(2026, time.August, 1, 12, 0, 0, 0, time.UTC)
	return &entity.TechCard{
		Id:          7,
		LockVersion: 4,
		TechCardInsert: entity.TechCardInsert{
			StyleNumber:       sql.NullString{String: "SS25-0042", Valid: true},
			StyleNumberSource: entity.StyleNumberSourceManual,
			Name:              "Source Style",
			SeasonCode:        sql.NullString{String: "SS", Valid: true},
			SeasonYear:        sql.NullInt32{Int32: 2025, Valid: true},
			Stage:             entity.TechCardStageProto,
			ApprovalState:     entity.TechCardApprovalReleased,
			MeasurementUnit:   entity.TechCardUnitMm,
			Purpose:           entity.TechCardPurposeSellable,
			Signoffs: []entity.TechCardSignoff{{
				Section:      entity.SignoffConstruction,
				State:        entity.SignoffStateApproved,
				SignedDigest: sql.NullString{String: "source-construction-digest", Valid: true},
			}},
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
}

func cloneRequest() *pb_admin.CloneStyleForSeasonRequest {
	return &pb_admin.CloneStyleForSeasonRequest{
		SourceStyleId:         7,
		ExpectedSourceVersion: 4,
		SkuSeason: &pb_common.SkuSeason{
			Code: pb_common.SeasonEnum_SEASON_ENUM_FW,
			Year: 2026,
		},
	}
}

func TestCloneStyleForSeasonUsesGeneratedNumberAndTransactionalSourceGuard(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	tc := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(tc)
	tc.EXPECT().GetTechCardByIdConsistent(mock.Anything, 7).Return(cloneSourceCard(), nil)
	tc.EXPECT().GetCostingFxRatesToBase(mock.Anything).Return(nil, nil)
	tc.EXPECT().SuggestStyleNumber(mock.Anything, "FW", 2026).Return("FW26-0007", nil)
	tc.EXPECT().CloneTechCardForSeason(mock.Anything, 7, 4, mock.AnythingOfType("*entity.TechCardInsert")).
		Run(func(_ context.Context, _, _ int, insert *entity.TechCardInsert) {
			require.Equal(t, "FW26-0007", insert.StyleNumber.String)
			require.True(t, insert.StyleNumber.Valid)
			require.Equal(t, entity.StyleNumberSourceGenerated, insert.StyleNumberSource)
			require.Equal(t, entity.TechCardApprovalDraft, insert.ApprovalState)
			require.Equal(t, entity.TechCardStageProto, insert.Stage, "clone preserves the source stage")
			require.Equal(t, "FW", insert.SeasonCode.String)
			require.Equal(t, int32(2026), insert.SeasonYear.Int32)
			require.Empty(t, insert.Signoffs, "a clone must not inherit source approvals")
		}).Return(11, nil)

	resp, err := (&Server{repo: repo}).CloneStyleForSeason(fullAccessCtx(), cloneRequest())
	require.NoError(t, err)
	require.Equal(t, int32(11), resp.NewStyleId)
}

func TestCloneStyleForSeasonRejectsSourceChangedBeforeInsert(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	tc := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(tc)
	tc.EXPECT().GetTechCardByIdConsistent(mock.Anything, 7).Return(cloneSourceCard(), nil)
	tc.EXPECT().GetCostingFxRatesToBase(mock.Anything).Return(nil, nil)
	tc.EXPECT().SuggestStyleNumber(mock.Anything, "FW", 2026).Return("FW26-0007", nil)
	tc.EXPECT().CloneTechCardForSeason(mock.Anything, 7, 4, mock.AnythingOfType("*entity.TechCardInsert")).
		Return(0, entity.ErrTechCardConflict)

	_, err := (&Server{repo: repo}).CloneStyleForSeason(fullAccessCtx(), cloneRequest())
	require.Equal(t, codes.Aborted, status.Code(err))
}

// Сезонный клон — это круговой рейс entity → pb → entity (style.go строит payload через
// ConvertEntityTechCardToPb и тут же скармливает его ConvertPbTechCardInsertToEntity), и парк
// оборудования обязан пережить его целиком: профили с ключами и именами, и ссылки шагов на эти
// ключи.
//
// Это НЕ дубль round-trip теста в dto. Там обёртку equipment_defaults собирает рука теста, поэтому
// эмиттер, который её не заполняет, выглядит там исправным. Здесь payload строит СЕРВЕР, и цена
// незаполненной обёртки видна только тут:
//   - пустая обёртка (или её отсутствие) — это «карточка без парка», и клон уезжает в новый сезон
//     без единой машинки;
//   - хуже того, пустая обёртка ещё и отцепляет ссылки шагов: resolveProfileKey не находит ключ в
//     пустом парке и ШТАТНО детачит его в NULL (правило S8, чтобы уборка дефолтов не блокировала
//     сохранение) — то есть потеря происходит по исправной ветке, без единой ошибки.
func TestCloneStyleForSeasonCarriesEquipmentParkAndStepProfileReferences(t *testing.T) {
	// Ключи профилей — те же durable 26-символьные ключи, что у строк BOM и деталей.
	const overlockKey = "OVERLOCKPROFILEKEY00000001"
	const ironKey = "IRONPRESSPROFILEKEY0000001"
	require.Len(t, overlockKey, 26)
	require.Len(t, ironKey, 26)

	source := cloneSourceCard()
	source.Construction = &entity.TechCardConstruction{
		Notes: sql.NullString{String: "общие заметки", Valid: true},
		EquipmentDefaults: &entity.TechCardEquipmentDefaults{
			Machines: []entity.TechCardMachineProfile{{
				ProfileKey:    overlockKey,
				Label:         sql.NullString{String: "оверлок у окна", Valid: true},
				MachineType:   "overlock",
				ThreadCount:   sql.NullInt32{Int32: 4, Valid: true},
				NeedleType:    sql.NullString{String: "ballpoint", Valid: true},
				NeedleSizeNm:  sql.NullInt32{Int32: 90, Valid: true},
				BedType:       sql.NullString{String: "flatbed", Valid: true},
				Automation:    sql.NullString{String: "semi_auto", Valid: true},
				StitchesPerCm: decimal.NullDecimal{Decimal: decimal.RequireFromString("4"), Valid: true},
				StitchWidthMm: decimal.NullDecimal{Decimal: decimal.RequireFromString("5"), Valid: true},
			}},
			Presses: []entity.TechCardPressProfile{{
				ProfileKey:         ironKey,
				Label:              sql.NullString{String: "утюг у стены", Valid: true},
				PressEquipment:     "iron",
				PressOperationType: sql.NullString{String: string(entity.OpTypeFusing), Valid: true},
				PressTemperatureC:  sql.NullInt32{Int32: 140, Valid: true},
				PressDwellSec:      sql.NullInt32{Int32: 10, Valid: true},
				// false, а не NULL: «без пара» — это инструкция, и клон обязан донести именно её.
				PressSteam: sql.NullBool{Bool: false, Valid: true},
				PressCloth: sql.NullString{String: "none", Valid: true},
			}},
		},
	}
	source.Operations = []entity.TechCardOperation{
		{
			OperationNumber:   sql.NullInt32{Int32: 10, Valid: true},
			OperationType:     entity.OpTypeMachine,
			MachineType:       sql.NullString{String: "overlock", Valid: true},
			MachineProfileKey: sql.NullString{String: overlockKey, Valid: true},
			// Override поверх профиля: он тоже обязан доехать, иначе клон «унаследует» не то.
			ThreadCount: sql.NullInt32{Int32: 3, Valid: true},
			Zone:        entity.ZoneOuter,
		},
		{
			OperationNumber: sql.NullInt32{Int32: 20, Valid: true},
			OperationType:   entity.OpTypeFusing,
			PressEquipment:  sql.NullString{String: "iron", Valid: true},
			PressProfileKey: sql.NullString{String: ironKey, Valid: true},
			Zone:            entity.ZoneInterlining,
		},
	}

	repo := mocks.NewMockRepository(t)
	tc := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(tc)
	tc.EXPECT().GetTechCardByIdConsistent(mock.Anything, 7).Return(source, nil)
	tc.EXPECT().GetCostingFxRatesToBase(mock.Anything).Return(nil, nil)
	tc.EXPECT().SuggestStyleNumber(mock.Anything, "FW", 2026).Return("FW26-0007", nil)
	tc.EXPECT().CloneTechCardForSeason(mock.Anything, 7, 4, mock.AnythingOfType("*entity.TechCardInsert")).
		Run(func(_ context.Context, _, _ int, insert *entity.TechCardInsert) {
			require.NotNil(t, insert.Construction, "the clone keeps the construction section")
			park := insert.Construction.EquipmentDefaults
			require.NotNil(t, park, "the clone must carry the equipment wrapper, not a nil one")

			require.Len(t, park.Machines, 1, "the machine park must survive the clone")
			m := park.Machines[0]
			require.Equal(t, overlockKey, m.ProfileKey, "the durable key is the identity a step points at")
			require.Equal(t, "оверлок у окна", m.Label.String)
			require.Equal(t, "overlock", m.MachineType)
			require.Equal(t, int32(4), m.ThreadCount.Int32)
			require.Equal(t, "ballpoint", m.NeedleType.String)
			require.Equal(t, int32(90), m.NeedleSizeNm.Int32)
			require.Equal(t, "flatbed", m.BedType.String)
			require.Equal(t, "semi_auto", m.Automation.String)
			require.True(t, m.StitchesPerCm.Valid)
			require.Equal(t, "4", m.StitchesPerCm.Decimal.String())
			require.Equal(t, "5", m.StitchWidthMm.Decimal.String())

			require.Len(t, park.Presses, 1, "the ВТО park must survive the clone")
			p := park.Presses[0]
			require.Equal(t, ironKey, p.ProfileKey)
			require.Equal(t, "утюг у стены", p.Label.String)
			require.Equal(t, "iron", p.PressEquipment)
			require.Equal(t, string(entity.OpTypeFusing), p.PressOperationType.String)
			require.Equal(t, int32(140), p.PressTemperatureC.Int32)
			require.Equal(t, int32(10), p.PressDwellSec.Int32)
			require.True(t, p.PressSteam.Valid, "«без пара» is an instruction; NULL would turn it into a default")
			require.False(t, p.PressSteam.Bool)
			require.Equal(t, "none", p.PressCloth.String)

			require.Len(t, insert.Operations, 2)
			machineStep := insert.Operations[0]
			require.Equal(t, entity.OpTypeMachine, machineStep.OperationType)
			require.Equal(t, "overlock", machineStep.MachineType.String)
			require.True(t, machineStep.MachineProfileKey.Valid,
				"the step's reference to a park profile must not detach in the clone")
			require.Equal(t, overlockKey, machineStep.MachineProfileKey.String)
			require.Equal(t, int32(3), machineStep.ThreadCount.Int32, "the step's own override, not the profile's 4")

			pressStep := insert.Operations[1]
			require.Equal(t, entity.OpTypeFusing, pressStep.OperationType)
			require.Equal(t, "iron", pressStep.PressEquipment.String)
			require.True(t, pressStep.PressProfileKey.Valid,
				"the ВТО step's reference to a park profile must not detach in the clone")
			require.Equal(t, ironKey, pressStep.PressProfileKey.String)
		}).Return(11, nil)

	_, err := (&Server{repo: repo}).CloneStyleForSeason(fullAccessCtx(), cloneRequest())
	require.NoError(t, err)
}

func TestCloneStyleForSeasonNormalizesLegacyIssueRefsAndCurrencylessCosting(t *testing.T) {
	source := cloneSourceCard()
	source.Operations = []entity.TechCardOperation{{
		OperationNumber: sql.NullInt32{Int32: 10, Valid: true},
		// The stored form after 0306: what the step does and what it does it on are two fields.
		OperationType: entity.OpTypeMachine,
		MachineType:   sql.NullString{String: "lockstitch", Valid: true},
		Zone:          entity.ZoneOuter,
	}}
	source.Callouts = []entity.TechCardCallout{{Number: 1}}
	source.Issues = []entity.TechCardIssue{{
		OperationNumber: sql.NullInt32{Int32: 7, Valid: true},
		CalloutNumber:   sql.NullInt32{Int32: 99, Valid: true},
		Description:     "legacy references",
		Severity:        entity.IssueSeverityMedium,
		Status:          entity.IssueStatusOpen,
	}}
	source.Costing = &entity.TechCardCosting{
		CmtCost: decimal.NullDecimal{Decimal: decimal.NewFromInt(12), Valid: true},
	}

	repo := mocks.NewMockRepository(t)
	tc := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(tc)
	tc.EXPECT().GetTechCardByIdConsistent(mock.Anything, 7).Return(source, nil)
	tc.EXPECT().GetCostingFxRatesToBase(mock.Anything).Return(nil, nil)
	tc.EXPECT().SuggestStyleNumber(mock.Anything, "FW", 2026).Return("FW26-0007", nil)
	tc.EXPECT().CloneTechCardForSeason(mock.Anything, 7, 4, mock.AnythingOfType("*entity.TechCardInsert")).
		Run(func(_ context.Context, _, _ int, insert *entity.TechCardInsert) {
			require.Nil(t, insert.Costing, "ambiguous currencyless costing is not copied")
			require.Len(t, insert.Issues, 1)
			require.False(t, insert.Issues[0].OperationNumber.Valid)
			require.False(t, insert.Issues[0].CalloutNumber.Valid)
		}).Return(11, nil)

	_, err := (&Server{repo: repo}).CloneStyleForSeason(fullAccessCtx(), cloneRequest())
	require.NoError(t, err)
}
