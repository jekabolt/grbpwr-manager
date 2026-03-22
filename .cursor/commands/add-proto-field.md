# Add a field to an existing proto message

1. Find the message in proto/common/common/*.proto or proto/admin/admin/admin.proto or proto/frontend/frontend/frontend.proto
2. Add the field with the next available field number
3. Run `make proto`
4. Update the entity struct in internal/entity/ with matching `db:"..."` tag
5. Update DTO conversion in internal/dto/ (ConvertPb*ToEntity* / ConvertEntityTo*)
6. Update store SQL queries in internal/store/ if it maps to a DB column
7. If a new DB column is needed, create a migration per @200-migration-workflow.mdc

See @100-protobuf.mdc for field naming and type conventions.
