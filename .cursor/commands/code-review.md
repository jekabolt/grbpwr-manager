# Code review against project conventions

Review the current changes against:

- @000-core.mdc: context first, decimal for money, sql.Null* for nullable DB fields, no float64 for money
- Layered architecture: proto → apisrv → dto → entity → store
- Functions under 80 lines, files under 500 lines
- Error wrapping with %w, gRPC status codes only at API layer

Flag any violations with specific file:line and suggested fix.
