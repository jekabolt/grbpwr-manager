package entity

import (
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func material(name, unit, qty string) SampleOutstandingMaterial {
	return SampleOutstandingMaterial{
		MaterialID: 7, Name: name, Unit: unit, Qty: decimal.RequireFromString(qty),
	}
}

// Пустой семпл удаляется, и вердикт при этом МОЛЧИТ: пустые категории в списки не попадают, иначе
// диалог печатал бы «0 задач осиротеет» и оператор научился бы пролистывать его вместе с
// настоящими строками.
func TestClassifySampleDeletionEmptyIsDeletableAndSilent(t *testing.T) {
	v := ClassifySampleDeletion(SampleDeletionFacts{SampleID: 3, Label: "#1"})
	require.True(t, v.Deletable)
	require.Empty(t, v.Blockers)
	require.Empty(t, v.Cascade)
	require.Empty(t, v.Orphans)
	require.Empty(t, v.BlockerSummary())
}

// НУЛЕВОЙ ОСТАТОК — не «движений не было», а «всё вернулось», и это разрешение, а не блокер. Ровно
// этот случай старое правило (COUNT(*) движений) и запрещало навсегда.
func TestClassifySampleDeletionReturnedMaterialUnblocks(t *testing.T) {
	v := ClassifySampleDeletion(SampleDeletionFacts{
		SampleID:  3,
		Materials: []SampleOutstandingMaterial{material("Wool Melton 340", "m", "0")},
		Orphans:   SampleOrphanCounts{MaterialMovements: 2},
	})
	require.True(t, v.Deletable)
	require.Empty(t, v.Blockers)
	// Движения пережили удаление и обязаны быть названы: их никто не стирал.
	require.Len(t, v.Orphans, 1)
	require.Equal(t, SampleOrphanMaterialMovement, v.Orphans[0].Reason)
	require.Equal(t, 2, v.Orphans[0].Count)
}

// Невозвращённый материал держит удаление и НАЗЫВАЕТ себя: количество, единица, имя. Без имени
// отказ был бы невыполним — оператор не знает, что именно возвращать.
func TestClassifySampleDeletionOutstandingMaterialBlocksAndNamesIt(t *testing.T) {
	v := ClassifySampleDeletion(SampleDeletionFacts{
		SampleID: 3,
		Materials: []SampleOutstandingMaterial{
			material("Wool Melton 340", "m", "2.400"),
			material("Кнопка 15 мм", "шт", "0"),
		},
	})
	require.False(t, v.Deletable)
	require.Len(t, v.Blockers, 1)
	require.Equal(t, SampleBlockerMaterialOutstanding, v.Blockers[0].Reason)
	require.Equal(t, 1, v.Blockers[0].Count, "считаем материалы с остатком, а не все строки ленты")
	// 2.400 в DECIMAL(12,3) — это то, как число ЛЕЖИТ, а не то, сколько отмерил оператор.
	require.Contains(t, v.Blockers[0].Text, "2.4 m «Wool Melton 340»")
	require.NotContains(t, v.Blockers[0].Text, "Кнопка")
}

// Возврат больше выдачи — тоже отказ, но разговор другой: лента разошлась, и семпл единственное,
// что объясняет лишний приход на складе.
func TestClassifySampleDeletionOverReturnIsItsOwnBlocker(t *testing.T) {
	v := ClassifySampleDeletion(SampleDeletionFacts{
		SampleID:  3,
		Materials: []SampleOutstandingMaterial{material("Wool Melton 340", "m", "-1.5")},
	})
	require.False(t, v.Deletable)
	require.Len(t, v.Blockers, 1)
	require.Equal(t, SampleBlockerMaterialOverReturn, v.Blockers[0].Reason)
	require.Contains(t, v.Blockers[0].Text, "1.5 m «Wool Melton 340»", "печатаем модуль, знак уже сказан словами")
}

// Обе причины приходят ЗА ОДИН заход. Оператор, снявший первую и узнавший вторую только со второй
// попытки, ходит два круга там, где сервер знал ответ целиком.
func TestClassifySampleDeletionReportsEveryBlockerAtOnce(t *testing.T) {
	v := ClassifySampleDeletion(SampleDeletionFacts{
		SampleID:  3,
		Materials: []SampleOutstandingMaterial{material("Wool Melton 340", "m", "2")},
		Fittings:  2,
	})
	require.False(t, v.Deletable)
	require.Len(t, v.Blockers, 2)
	require.Equal(t, SampleBlockerMaterialOutstanding, v.Blockers[0].Reason)
	require.Equal(t, SampleBlockerFitting, v.Blockers[1].Reason)
	require.Contains(t, v.Blockers[1].Text, "2 примерки")
	require.Contains(t, v.BlockerSummary(), "; ")
}

// Совет по материалу — единственное место, где статус «списан» вообще что-то меняет: склад
// откажет в возврате по списанному семплу, и совет «верните материал» был бы невыполним.
func TestSampleFieldViolationsAdviseByBlockerAndStatus(t *testing.T) {
	v := ClassifySampleDeletion(SampleDeletionFacts{
		SampleID:  3,
		Materials: []SampleOutstandingMaterial{material("Wool Melton 340", "m", "2")},
		Fittings:  1,
	})

	live := v.FieldViolations(false)
	require.Len(t, live, 2)
	require.Contains(t, live[0].Message, sampleFixReturnMaterial)
	require.Contains(t, live[1].Message, sampleFixDeleteFittings)

	scrapped := v.FieldViolations(true)
	require.Contains(t, scrapped[0].Message, sampleFixReturnScrapped)
	// Примерок статус не касается: их удаляют независимо от того, что стало с семплом.
	require.Contains(t, scrapped[1].Message, sampleFixDeleteFittings)
}

// Материал без имени в справочнике всё равно должен быть адресуем: «материал #7» — это то, что
// оператор найдёт, а пустые кавычки — нет.
func TestClassifySampleDeletionUnnamedMaterialStaysAddressable(t *testing.T) {
	v := ClassifySampleDeletion(SampleDeletionFacts{
		SampleID:  3,
		Materials: []SampleOutstandingMaterial{{MaterialID: 7, Qty: decimal.NewFromInt(3)}},
	})
	require.False(t, v.Deletable)
	require.Contains(t, v.Blockers[0].Text, "материал #7")
	require.NotContains(t, v.Blockers[0].Text, "«»")
}

// Каскад и сироты — разные категории, и вердикт не имеет права их путать: первое умрёт, второе
// переживёт удаление и потеряет семпл.
func TestClassifySampleDeletionSeparatesCascadeFromOrphans(t *testing.T) {
	v := ClassifySampleDeletion(SampleDeletionFacts{
		SampleID: 3,
		Cascade:  SampleCascadeCounts{Media: 3, Substitutions: 1},
		Orphans:  SampleOrphanCounts{MaterialMovements: 4, DevExpenses: 2, Tasks: 1, NextRounds: 1},
	})
	require.True(t, v.Deletable)
	require.Len(t, v.Cascade, 2)
	require.Equal(t, SampleCascadeMedia, v.Cascade[0].Reason)
	require.Contains(t, v.Cascade[0].Text, "3 фотографии семпла")
	require.Equal(t, SampleCascadeSubstitution, v.Cascade[1].Reason)

	require.Len(t, v.Orphans, 4)
	reasons := make([]string, 0, len(v.Orphans))
	for _, o := range v.Orphans {
		reasons = append(reasons, o.Reason)
	}
	require.Equal(t, []string{
		SampleOrphanMaterialMovement, SampleOrphanDevExpense, SampleOrphanTask, SampleOrphanNextRound,
	}, reasons)
	// Деньги остаются деньгами карточки — вердикт обязан сказать именно это, а не «пропадут».
	require.Contains(t, v.Orphans[1].Text, "останутся на карточке")
}
