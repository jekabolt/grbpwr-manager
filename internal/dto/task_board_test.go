package dto

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

func TestConvertPbTaskBoardRefToEntity(t *testing.T) {
	cases := []struct {
		name    string
		id      int32
		legacy  pb_common.TaskBoard
		want    entity.TaskBoardRef
		wantErr bool
	}{
		{"id wins over enum", 9, pb_common.TaskBoard_TASK_BOARD_DESIGN, entity.TaskBoardRef{Id: 9}, false},
		{"id alone", 9, pb_common.TaskBoard_TASK_BOARD_UNKNOWN, entity.TaskBoardRef{Id: 9}, false},
		{"legacy enum maps to slug", 0, pb_common.TaskBoard_TASK_BOARD_SOURCING, entity.TaskBoardRef{Slug: entity.TaskBoardSourcing}, false},
		{"both unset", 0, pb_common.TaskBoard_TASK_BOARD_UNKNOWN, entity.TaskBoardRef{}, false},
		{"negative id", -1, pb_common.TaskBoard_TASK_BOARD_DESIGN, entity.TaskBoardRef{}, true},
		{"unmapped enum", 0, pb_common.TaskBoard(99), entity.TaskBoardRef{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ConvertPbTaskBoardRefToEntity(tc.id, tc.legacy)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestValidateTaskBoardName(t *testing.T) {
	if got, err := ValidateTaskBoardName("  samples\t"); err != nil || got != "samples" {
		t.Errorf("trim: got %q, %v", got, err)
	}
	if _, err := ValidateTaskBoardName(" \n "); err == nil {
		t.Error("blank name must fail")
	}
	if _, err := ValidateTaskBoardName("bad\xffname"); err == nil {
		t.Error("invalid UTF-8 must fail")
	}
	// The limit counts characters, so 64 Cyrillic letters (128 bytes) still fit VARCHAR(64).
	if _, err := ValidateTaskBoardName(strings.Repeat("ж", entity.TaskBoardNameMaxLen)); err != nil {
		t.Errorf("64 characters must pass: %v", err)
	}
	if _, err := ValidateTaskBoardName(strings.Repeat("ж", entity.TaskBoardNameMaxLen+1)); err == nil {
		t.Error("65 characters must fail")
	}
}

func TestConvertEntityTaskBoardToPb(t *testing.T) {
	if ConvertEntityTaskBoardToPb(nil) != nil {
		t.Fatal("nil board must map to nil")
	}
	seeded := ConvertEntityTaskBoardToPb(&entity.TaskBoardEntry{
		Id: 6, Name: "Контент", Slug: sql.NullString{String: "content", Valid: true}, Position: 5, TaskCount: 3, ArchivedTaskCount: 2,
	})
	if seeded.LegacyBoard != pb_common.TaskBoard_TASK_BOARD_CONTENT || seeded.Name != "Контент" ||
		seeded.Position != 5 || seeded.TaskCount != 3 || seeded.ArchivedTaskCount != 2 {
		t.Errorf("seeded board: %+v", seeded)
	}
	custom := ConvertEntityTaskBoardToPb(&entity.TaskBoardEntry{Id: 7, Name: "samples", CreatedBy: "olya"})
	if custom.LegacyBoard != pb_common.TaskBoard_TASK_BOARD_UNKNOWN || custom.Id != 7 || custom.CreatedBy != "olya" {
		t.Errorf("custom board: %+v", custom)
	}
}

// A task on a custom board carries its board_id and an UNKNOWN legacy enum ('custom-<id>' has no enum).
func TestTaskConvertersCarryBoardId(t *testing.T) {
	custom := entity.TaskBoardLegacyValue(7, sql.NullString{})
	if custom != "custom-7" {
		t.Fatalf("legacy value of a custom board = %q", custom)
	}
	if v := entity.TaskBoardLegacyValue(2, sql.NullString{String: "design", Valid: true}); v != entity.TaskBoardDesign {
		t.Fatalf("legacy value of a seeded board = %q", v)
	}

	pt := ConvertEntityTaskToPb(&entity.Task{Id: 1, Board: custom, BoardId: 7})
	if pt.BoardId != 7 || pt.Board != pb_common.TaskBoard_TASK_BOARD_UNKNOWN {
		t.Errorf("task: board_id=%d board=%v", pt.BoardId, pt.Board)
	}
	seeded := ConvertEntityTaskToPb(&entity.Task{Id: 2, Board: entity.TaskBoardDesign, BoardId: 2})
	if seeded.BoardId != 2 || seeded.Board != pb_common.TaskBoard_TASK_BOARD_DESIGN {
		t.Errorf("seeded task: board_id=%d board=%v", seeded.BoardId, seeded.Board)
	}

	links := ConvertEntityTaskLinksToPb([]entity.TaskLink{{TaskId: 3, Board: custom, BoardId: 7}})
	if links[0].BoardId != 7 || links[0].Board != pb_common.TaskBoard_TASK_BOARD_UNKNOWN {
		t.Errorf("link: %+v", links[0])
	}
	files := ConvertEntityLibraryFileTasksToPb([]entity.LibraryFileTask{{TaskId: 4, Board: entity.TaskBoardContent, BoardId: 6}})
	if files[0].BoardId != 6 || files[0].Board != pb_common.TaskBoard_TASK_BOARD_CONTENT {
		t.Errorf("library file task: %+v", files[0])
	}
}
