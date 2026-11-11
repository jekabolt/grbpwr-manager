package dto

import (
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
)

// TechCardSeamsChangeFromPb converts a ChangeTechCardSeams request and validates it (everything but
// what needs the card: step numbers, link keys, piece membership). Upsert rows go through the same
// conversion and checks as UpsertTechCardSeams, with field paths naming upsert[i].
func TechCardSeamsChangeFromPb(req *pb_admin.ChangeTechCardSeamsRequest, by string) (entity.TechCardSeamsChange, error) {
	// Bounds before any copy, as for the upsert.
	for _, b := range []struct {
		field string
		n     int
	}{{"delete", len(req.GetDelete())}, {"link", len(req.GetLink())}, {"detach", len(req.GetDetach())}} {
		if b.n > entity.TechCardSeamChangeMaxRefs {
			return entity.TechCardSeamsChange{}, entity.NewFieldViolation(b.field, "too_many", "",
				fmt.Sprintf("at most %d per call", entity.TechCardSeamChangeMaxRefs))
		}
	}
	in := entity.TechCardSeamsChange{
		TechCardId: int(req.GetTechCardId()),
		Delete:     append([]string(nil), req.GetDelete()...),
		Link:       seamLinksFromPb(req.GetLink()),
		Detach:     seamLinksFromPb(req.GetDetach()),
		By:         by,
	}
	if req.ExpectedLockVersion != nil {
		v := int(req.GetExpectedLockVersion())
		in.ExpectedLockVersion = &v
	}
	if len(req.GetUpsert()) > 0 {
		w, err := TechCardSeamsWriteFromPb(int(req.GetTechCardId()), req.GetUpsert(), by)
		if err != nil {
			return entity.TechCardSeamsChange{}, entity.RenameSeamFieldPrefix(err, "seams", "upsert")
		}
		in.Upsert = w.Seams
	}
	if err := entity.ValidateTechCardSeamsChange(&in); err != nil {
		return entity.TechCardSeamsChange{}, err
	}
	return in, nil
}

func seamLinksFromPb(links []*pb_admin.SeamLink) []entity.TechCardSeamLink {
	out := make([]entity.TechCardSeamLink, 0, len(links))
	for _, l := range links {
		out = append(out, entity.TechCardSeamLink{OperationNumber: int(l.GetOperationNumber()), SeamKey: l.GetSeamKey()})
	}
	return out
}

// TechCardSeamsChangeToPb is the response: the full list, every numbered step's links, the version
// after the change, the key map and the removed keys.
func TechCardSeamsChangeToPb(r entity.TechCardSeamsChangeResult) *pb_admin.ChangeTechCardSeamsResponse {
	links := make([]*pb_admin.OperationSeamKeys, 0, len(r.Links))
	for _, l := range r.Links {
		links = append(links, &pb_admin.OperationSeamKeys{
			OperationNumber: int32(l.OperationNumber),
			SeamKeys:        append([]string{}, l.SeamKeys...),
		})
	}
	keyMap := make([]*pb_admin.SeamKeyMap, 0, len(r.KeyMap))
	for _, m := range r.KeyMap {
		keyMap = append(keyMap, &pb_admin.SeamKeyMap{Sent: m.Sent, Stored: m.Stored})
	}
	return &pb_admin.ChangeTechCardSeamsResponse{
		Seams:       TechCardSeamsToPb(r.Seams),
		Links:       links,
		LockVersion: int32(r.LockVersion),
		KeyMap:      keyMap,
		DeletedKeys: append([]string{}, r.DeletedKeys...),
	}
}
