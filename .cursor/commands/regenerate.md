# Regenerate code

Run these in order:

1. `make proto` — regenerates proto/gen from proto/**/*.proto
2. `make generate-mocks` — regenerates internal/dependency/mocks from dependency.go
3. `make internal/statics` — regenerates swagger JSON (or `make build` does all)

Never edit proto/gen/, internal/dependency/mocks/, openapi/gen/resend/, internal/api/http/static/swagger/ manually.
