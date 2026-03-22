# Add new gRPC RPC

1. Add RPC definition in proto/admin/admin/admin.proto or proto/frontend/frontend/frontend.proto with google.api.http annotation
2. Add request/response messages in proto/common or the service proto
3. Run `make proto`
4. Add interface method in internal/dependency/dependency.go, run `make generate-mocks`
5. Implement store method in internal/store/
6. Add DTO conversion in internal/dto/
7. Implement handler in internal/apisrv/admin/ or frontend/
8. Set gRPC status codes only at API layer

See @100-grpc-api.mdc and @100-protobuf.mdc.
