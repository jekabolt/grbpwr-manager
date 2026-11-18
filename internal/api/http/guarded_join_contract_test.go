package httpapi

import (
	"net/http"
	"slices"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

func TestSavedJoinRequiresSparseUpdateTechCardGuard(t *testing.T) {
	const body = `{"id":156,"expectedLockVersion":9,"guardedPieceJoin":{"expectedRow":{"scopeKey":"main","patternLineKey":"01JABCDEFGHJKMNPQRSTVWXYZ0"},"resultLineKey":"01JABCDEFGHJKMNPQRSTVWXYZB"}}`
	stub := &techCardStubServer{}
	server := serveAdmin(t, newAdminServeMux(), stub)
	code, reply := postTechCardUpdate(t, server, body)
	require.Equal(t, http.StatusOK, code, reply)
	require.Equal(t, 1, stub.updateCalls)
	require.NotNil(t, stub.lastUpdate.GetGuardedPieceJoin())
	require.Nil(t, stub.lastUpdate.GetTechCard())
	require.Nil(t, stub.lastUpdate.GetGuardedPieceRename())
	current := stub.lastUpdate.ProtoReflect().Descriptor()
	require.EqualValues(t, 7, current.Fields().ByName("guarded_piece_join").Number())

	// Reconstruct the preceding request descriptor, then exercise both historical parsers.
	fd := protodesc.ToFileDescriptorProto(current.ParentFile())
	for _, m := range fd.MessageType {
		if m.GetName() == "UpdateTechCardRequest" {
			m.Field = slices.DeleteFunc(m.Field, func(f *descriptorpb.FieldDescriptorProto) bool { return f.GetNumber() == 7 })
		}
	}
	oldFile, e := protodesc.NewFile(fd, protoregistry.GlobalFiles)
	require.NoError(t, e)
	old := dynamicpb.NewMessage(oldFile.Messages().ByName("UpdateTechCardRequest"))
	require.Error(t, protojson.Unmarshal([]byte(body), old))
	require.NoError(t, (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal([]byte(body), old))
	wire, e := proto.Marshal(old)
	require.NoError(t, e)
	var projected pb_admin.UpdateTechCardRequest
	require.NoError(t, proto.Unmarshal(wire, &projected))
	require.Nil(t, projected.GetTechCard())
	require.Nil(t, projected.GetGuardedPieceRename())
	require.Nil(t, projected.GetGuardedPieceJoin())
	_, e = dto.ConvertPbTechCardInsertToEntity(projected.GetTechCard())
	require.Error(t, e)
}
