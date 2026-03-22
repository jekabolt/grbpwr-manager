# Add new full-stack feature

Follow @400-new-feature-workflow.mdc. Work top-down:

1. Proto (if API): add/modify messages in proto/common, add RPC in admin or frontend proto, run `make proto`
2. Entity: add structs in internal/entity/<domain>.go (*Insert / *Full naming)
3. Migration: if schema changes, create migration per @200-migration-workflow.mdc
4. Dependency: add method to interface in internal/dependency/dependency.go, run `make generate-mocks`
5. Store: implement in internal/store/<domain>.go
6. DTO: add conversions in internal/dto/ if API-exposed
7. API handler: implement in internal/apisrv/admin/ or frontend/
8. Tests: unit tests with mockery mocks

Search for similar existing features first. Follow @000-core.mdc for context-first signatures, decimal.Decimal, sql.Null*.
