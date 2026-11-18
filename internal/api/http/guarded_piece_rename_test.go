package httpapi

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/dynamicpb"
)

const guardedRenameBody = `{"id":12,"expectedLockVersion":14,"guardedPieceRename":{"pieceLineKey":"01ARZ3NDEKTSV4RRFFQ69G5FAV","expectedName":"Front","nextName":"Передняя половинка"}}`

func TestGuardedZeroOperationPieceRenameGatewayCompatibility(t *testing.T) {
	stub := &techCardStubServer{}
	server := serveAdmin(t, newAdminServeMux(), stub)
	code, body := postTechCardUpdate(t, server, guardedRenameBody)
	if code != 200 || stub.updateCalls != 1 || stub.lastUpdate.GetTechCard() != nil ||
		stub.lastUpdate.GetGuardedPieceRename().GetNextName() != "Передняя половинка" {
		t.Fatalf("current gateway lost sparse guarded request: %d %s", code, body)
	}
	// The previous request descriptor is field-compatible apart from the new command.
	desc := (&pb_admin.UpdateTechCardRequest{}).ProtoReflect().Descriptor()
	oldFile := protodesc.ToFileDescriptorProto(desc.ParentFile())
	for _, message := range oldFile.MessageType {
		if message.GetName() == string(desc.Name()) {
			fields := message.Field[:0]
			for _, field := range message.Field {
				if field.GetName() != "guarded_piece_rename" {
					fields = append(fields, field)
				}
			}
			message.Field = fields
		}
	}
	old, err := protodesc.NewFile(oldFile, protoregistry.GlobalFiles)
	if err != nil {
		t.Fatal(err)
	}
	oldRequest := dynamicpb.NewMessage(old.Messages().ByName(desc.Name()))
	if err := protojson.Unmarshal([]byte(guardedRenameBody), oldRequest); err == nil {
		t.Fatal("old production decoder accepted unknown command")
	}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal([]byte(guardedRenameBody), oldRequest); err != nil {
		t.Fatal(err)
	}
	if oldRequest.Has(oldRequest.Descriptor().Fields().ByName("tech_card")) {
		t.Fatal("old permissive decoder produced a destructive legacy body")
	}
	if _, err := dto.ConvertPbTechCardInsertToEntity(nil); err == nil {
		t.Fatal("legacy writer accepted the resulting missing body")
	}
}
