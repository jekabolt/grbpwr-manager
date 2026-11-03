package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/apisrv/apierr"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// validateLabelsMockupSignGate — owner decision D-04 (labels rework, 0386): every garment label must
// carry a mockup, and the LABELS sign-off cannot be APPROVED while one does not. Only a FRESH approval
// is refused: saving is never blocked (a label is legitimately added before its artwork exists), and a
// carried approval is not re-judged — a label added after the approval makes the section read
// «changed since sign-off» through the digest instead.
//
// The labels are those of THIS payload: an approval is taken over what the payload carries (the
// presence belt refuses a LABELS approval from a payload that does not carry the labels).
func validateLabelsMockupSignGate(tc *entity.TechCardInsert,
	fresh map[entity.TechCardSignoffSection]bool) *entity.ValidationError {
	if tc == nil || !fresh[entity.SignoffLabels] {
		return nil
	}
	for i := range tc.Signoffs {
		so := tc.Signoffs[i]
		if so.Section != entity.SignoffLabels || so.State != entity.SignoffStateApproved {
			continue
		}
		missing := dto.GarmentLabelsWithoutMockup(tc)
		if len(missing) == 0 {
			return nil
		}
		return entity.NewFieldViolation(fmt.Sprintf("signoffs[%d].section", i),
			"cannot approve labels: labels without a mockup", strings.Join(missing, ", "),
			"add a mockup to each of these labels, then approve")
	}
	return nil
}

// validateFreshLabelsSectionsCarried is the presence belt of the labels rework (0386), on the update
// path only (a new card has nothing stored to keep). The composition label record is kept when the
// payload omits it (nil), and the garment labels / packaging items are kept when the payload lacks
// labels_aware. An approval taken over such a payload would fingerprint «nothing» over content the
// card still holds — a signature born stale. So a fresh LABELS / PACKAGING approval is refused exactly
// when the payload would leave STORED content of that section in place; a card that has none is
// approved as before, from any client.
func validateFreshLabelsSectionsCarried(tc, stored *entity.TechCardInsert,
	fresh map[entity.TechCardSignoffSection]bool) *entity.ValidationError {
	if tc == nil || stored == nil {
		return nil
	}
	for i := range tc.Signoffs {
		so := tc.Signoffs[i]
		if so.State != entity.SignoffStateApproved || !fresh[so.Section] {
			continue
		}
		kept := false
		switch so.Section {
		case entity.SignoffLabels:
			kept = (!tc.LabelsAware && len(stored.GarmentLabels) > 0) || (tc.CareLabel == nil && stored.CareLabel != nil)
		case entity.SignoffPackaging:
			kept = !tc.LabelsAware && len(stored.PackagingItems) > 0
		}
		if kept {
			return entity.NewFieldViolation(fmt.Sprintf("signoffs[%d].section", i),
				fmt.Sprintf("cannot approve %s because this save does not carry its labels / items", so.Section), "",
				"update the admin panel (hard-refresh) and approve again")
		}
	}
	return nil
}

// validateLabelsBornLinesSignGate refuses a fresh LABELS approval over a row whose only BOM link is the
// key of a line born in this same save. The read will report the new line's id, which the write cannot
// hash yet, so the signature would be born stale. The save itself is never refused over it: the row
// goes in, and the approval is one more save away.
func validateLabelsBornLinesSignGate(tc *entity.TechCardInsert,
	fresh map[entity.TechCardSignoffSection]bool) *entity.ValidationError {
	if tc == nil || !fresh[entity.SignoffLabels] {
		return nil
	}
	for i := range tc.Signoffs {
		so := tc.Signoffs[i]
		if so.Section != entity.SignoffLabels || so.State != entity.SignoffStateApproved {
			continue
		}
		born := dto.LabelsLinkedToBornBomLines(tc)
		if len(born) == 0 {
			return nil
		}
		return entity.NewFieldViolation(fmt.Sprintf("signoffs[%d].section", i),
			"cannot approve labels: material added in this save", strings.Join(born, ", "),
			"save the card first, then approve")
	}
	return nil
}

// garmentLabelFilesVisibleGate requires every library file this save adds to a label to be visible to
// the caller. The response of a card carries each file's public link, so without this an id typed by
// hand would read back the secret link of a file its author cannot see. A file already on this card
// is not asked again: a colleague's edit of an unrelated field must not fail over a file they cannot
// open. stored may be nil on create.
func (s *Server) garmentLabelFilesVisibleGate(ctx context.Context, stored, tc *entity.TechCardInsert) error {
	if tc == nil {
		return nil
	}
	known := map[int]bool{}
	if stored != nil {
		for i := range stored.GarmentLabels {
			for _, f := range stored.GarmentLabels[i].Files {
				known[f.LibraryFileId] = true
			}
		}
	}
	for i := range tc.GarmentLabels {
		for j, f := range tc.GarmentLabels[i].Files {
			if known[f.LibraryFileId] {
				continue
			}
			if _, err := s.repo.Files().GetFileById(ctx, f.LibraryFileId); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return apierr.Invalid(entity.NewFieldViolation(
						fmt.Sprintf("garment_labels[%d].files[%d].library_file_id", i, j),
						"file not found", fmt.Sprint(f.LibraryFileId), "upload the file again"))
				}
				slog.Default().ErrorContext(ctx, "can't check garment label file",
					slog.Int("library_file_id", f.LibraryFileId), slog.String("err", err.Error()))
				return status.Error(codes.Internal, "can't check label files; try again")
			}
			known[f.LibraryFileId] = true
		}
	}
	return nil
}

// dropMissingGarmentLabelFiles is the snapshot restore's part: a snapshot may name a library file
// deleted since (a file is refused deletion only while a row holds it). The row goes back without it
// rather than failing the whole restore on a foreign key. The tile then reads as empty.
func (s *Server) dropMissingGarmentLabelFiles(ctx context.Context, tc *entity.TechCardInsert) error {
	if tc == nil {
		return nil
	}
	var ids []int
	for i := range tc.GarmentLabels {
		for _, f := range tc.GarmentLabels[i].Files {
			ids = append(ids, f.LibraryFileId)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	alive, err := s.repo.TechCards().ExistingLibraryFileIds(ctx, ids)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't check garment label files of a snapshot", slog.String("err", err.Error()))
		return status.Error(codes.Internal, "can't check label files; try again")
	}
	for i := range tc.GarmentLabels {
		l := &tc.GarmentLabels[i]
		kept := l.Files[:0:0]
		for _, f := range l.Files {
			if alive[f.LibraryFileId] {
				kept = append(kept, f)
			}
		}
		l.Files = kept
	}
	return nil
}

// fillGarmentLabelFileLinks mints the public link of each label file on a card read for a RESPONSE.
// It is the only place that sets PublicURL: the snapshot and the archive convert the entity without it
// and stay token-free. A file with no live link stays empty, so nothing prints a QR to nowhere.
func (s *Server) fillGarmentLabelFileLinks(tc *entity.TechCard) {
	if tc == nil {
		return
	}
	for i := range tc.GarmentLabels {
		files := tc.GarmentLabels[i].Files
		for j := range files {
			if files[j].LinkEpoch > 0 {
				files[j].PublicURL = s.fileLinks.LinkURL(files[j].LibraryFileId, files[j].LinkEpoch)
			}
		}
	}
}
