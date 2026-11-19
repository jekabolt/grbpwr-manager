package provision_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// sevenActions proves a freshly provisioned team works: through the store, under ctx (which carries
// the team), it creates a task, a file record, a fit model, a tech card, a fitting, a material and a
// production run, and reads each back. ownerHandle is the team's only admin (username).
func sevenActions(t *testing.T, ctx context.Context, repo dependency.Repository, db dependency.DB, ownerHandle string) {
	t.Helper()
	suffix := time.Now().UnixNano()

	// Reference data comes from the team's own size dictionary (the process-wide dictionary cache
	// is team #1's until B7); no hard-coded ids.
	var sizeID int
	if err := db.GetContext(ctx, &sizeID, `SELECT id FROM size ORDER BY id LIMIT 1`); err != nil {
		t.Fatalf("size dictionary: %v", err)
	}

	// 1. task
	taskID, err := repo.Tasks().AddTask(ctx, &entity.Task{
		TaskInsert: entity.TaskInsert{
			Title:    fmt.Sprintf("seven-actions task %d", suffix),
			Priority: entity.TaskPriorityUnknown,
		},
		Board:     entity.TaskBoardDesign,
		Status:    entity.TaskStatusTodo,
		CreatedBy: ownerHandle,
	})
	if err != nil {
		t.Fatalf("action 1 (create task): %v", err)
	}
	if _, err := repo.Tasks().GetTaskById(ctx, taskID); err != nil {
		t.Fatalf("action 1 (read task back): %v", err)
	}

	// 2. file record (library row only, no object in the bucket)
	fileID, err := repo.Files().AddFile(ctx, &entity.LibraryFileInsert{
		ObjectKey:   fmt.Sprintf("seven-actions/%d.txt", suffix),
		FileName:    "seven-actions.txt",
		ContentType: "text/plain",
		SizeBytes:   1,
		Sha256:      fmt.Sprintf("%064d", suffix),
		UploadedBy:  ownerHandle,
	}, nil, nil)
	if err != nil {
		t.Fatalf("action 2 (create file record): %v", err)
	}
	if _, err := repo.Files().GetFileById(ctx, fileID); err != nil {
		t.Fatalf("action 2 (read file record back): %v", err)
	}

	// 3. fit model
	modelID, err := repo.Models().AddModel(ctx, &entity.ModelInsert{
		Name: fmt.Sprintf("seven-actions model %d", suffix),
		Measurements: []entity.ModelMeasurement{
			{Name: entity.BodyChest, ValueMM: 900},
		},
		BodyMeasurementSchemaVersion: entity.BodyMeasurementSchemaVersionP1,
	})
	if err != nil {
		t.Fatalf("action 3 (create fit model): %v", err)
	}
	if _, err := repo.Models().GetModelById(ctx, modelID); err != nil {
		t.Fatalf("action 3 (read fit model back): %v", err)
	}

	// 4. tech card
	techCardID, err := repo.TechCards().AddTechCard(ctx, &entity.TechCardInsert{
		StyleNumber:     sql.NullString{String: fmt.Sprintf("SEVEN-%d", suffix), Valid: true},
		Name:            "seven-actions card",
		Stage:           entity.TechCardStageProto,
		ApprovalState:   entity.TechCardApprovalDraft,
		MeasurementUnit: entity.TechCardUnitMm,
		SizeIds:         []int{sizeID},
		CreatedBy:       ownerHandle,
		UpdatedBy:       ownerHandle,
	})
	if err != nil {
		t.Fatalf("action 4 (create tech card): %v", err)
	}
	if _, err := repo.TechCards().GetTechCardById(ctx, techCardID); err != nil {
		t.Fatalf("action 4 (read tech card back): %v", err)
	}

	// 5. fitting (needs the tech card and the model from actions 3-4)
	fittingID, err := repo.Fittings().AddFitting(ctx, &entity.FittingInsert{
		TechCardId:  sql.NullInt32{Int32: int32(techCardID), Valid: true},
		ModelId:     sql.NullInt32{Int32: int32(modelID), Valid: true},
		FittingDate: time.Now().UTC(),
		Status:      entity.FittingPlanned,
		Verdict:     entity.FittingPending,
		CreatedBy:   ownerHandle,
		UpdatedBy:   ownerHandle,
	})
	if err != nil {
		t.Fatalf("action 5 (create fitting): %v", err)
	}
	if _, err := repo.Fittings().GetFittingById(ctx, fittingID); err != nil {
		t.Fatalf("action 5 (read fitting back): %v", err)
	}

	// 6. material
	materialID, err := repo.TechCards().CreateMaterial(ctx, &entity.MaterialInsert{
		Name:    fmt.Sprintf("seven-actions fabric %d", suffix),
		Section: "fabric",
	})
	if err != nil {
		t.Fatalf("action 6 (create material): %v", err)
	}
	if _, err := repo.TechCards().GetMaterial(ctx, materialID); err != nil {
		t.Fatalf("action 6 (read material back): %v", err)
	}

	// 7. production run (needs the tech card from action 4)
	runID, err := repo.ProductionRuns().CreateProductionRun(ctx, &entity.ProductionRunInsert{
		TechCardId: techCardID,
		Status:     entity.ProductionRunPlanned,
	})
	if err != nil {
		t.Fatalf("action 7 (create production run): %v", err)
	}
	if _, err := repo.ProductionRuns().GetProductionRun(ctx, runID); err != nil {
		t.Fatalf("action 7 (read production run back): %v", err)
	}
}
