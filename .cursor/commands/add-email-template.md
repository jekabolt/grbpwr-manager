# Add a new email template

Follow @200-mail.mdc for patterns.

1. Create `internal/mail/templates/<name>.gohtml` — use html/template, reference partials via `{{template "partials/..." .}}`
2. Add a `templateName` const and `templateSubjects` entry in `internal/mail/send.go`
3. Add exported `Send<Name>` and/or `Queue<Name>` method on Mailer (immediate vs worker-deferred)
4. Add the method to `dependency.Mailer` interface in `internal/dependency/dependency.go`
5. Run `make generate-mocks`
6. Create a DTO struct in `internal/dto/` for template data if needed
7. Wire the call site (apisrv handler, worker, etc.)

Use `sendWithInsert` for immediate send with DB persistence, `queueEmail` for fire-and-forget worker pickup.
