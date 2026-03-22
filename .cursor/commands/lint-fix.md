# Lint and fix

1. Run `make lint` to execute golangci-lint on internal/...
2. Parse the output for violations
3. Fix each violation following @000-core.mdc conventions
4. Re-run `make lint` to confirm all clean

Common fixes:
- Unused variables/imports: remove them
- Error not checked: add `if err != nil` handling
- Shadow declarations: rename the inner variable
- Function too long: extract helper functions (keep under 80 lines)
