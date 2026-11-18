# Saved native Join on UpdateTechCard

The guarded Join uses field 7, `guarded_piece_join`, on the ordinary `POST /api/admin/tech-card/update` request. It retires two exact saved piece lines and their scoped aliases, creates one separately keyed piece, and replaces the file on the existing pattern line. The ordinary upload, document CAS, revision history and snapshot owners perform the writes. The frontend Draft control still requires an explicit manual Wizard Apply hookup.

## Request

Send the card ID, expected lock version and the guarded message. The gateway rejects combinations with a full card, guarded rename, save session or snapshot reason. An older strict gateway rejects the unknown field. An older permissive gateway discards it and the ordinary document converter rejects the resulting absent document.

| Binding | Fields |
| --- | --- |
| Existing pattern | Exact card, normalized scope, pattern line key, saved-row revision, lock, pattern version, managed URL, size and SHA-256 |
| Assembly | Server revision and complete-source SHA from the saved Assembly snapshot |
| Source selections | Fixed and moving piece line keys, native identities, complete source-curve IDs and directed start/end vertex IDs |
| Registration | Current BOM line and material, grade token and dictionary ID, explicit translation in millionths of a millimetre |
| Reviewed result | Fresh piece line key, unused native identity, and the ordinary upload URL, filename, size and final Wizard SHA-256 |

The source profile accepts two distinct zero-allowance rectangles in the same material and single rank. The fixed right edge and moving left edge must coincide completely after translation, with matching directed grain. It accepts disjoint rectangular interior holes, three-point open Darts, native drills, text and point marks. Saved source lines must have the ordinary single-copy identical cut symmetry and normalized default lengthwise grain. The selected rank must exist in the current card range and in its size dictionary. Additional ranks on other material rows retain their identity and geometry.

## Transaction

The API reconstructs the proposed delta from the saved card and uses the ordinary validation and costing permissions. The store locks the card before inspecting Assembly and sidecar data. It resolves each source alias group from stored rows and validates the selected dictionary rank under a shared lock. The same transaction rechecks the complete-source digest and Assembly clock, all durable seam sides including unowned POSEs, raw operation annotations, hidden recipes and independently saved attachments.

The store reads both DXFs from the configured managed pattern bucket. Its verifier checks the full byte lengths and SHA-256s, the writer's compressed manifests, native block ownership and insertions, original curve and vertex identities, translated contours, signed holes and all supported marks. It signs and verifies a canonical two-source envelope with a request-local Ed25519 key. The saved history and snapshot retain the original file and pre-save document. The signature is transient.

Before commit, the store reloads the new card in the same transaction. It checks both retirements, the fresh child key and ID, its unique scoped alias, the reviewed file on the same pattern row, and the old file in its snapshot-linked history. The ordinary writer advances saved-row revisions on other pattern rows. Their native files, lexical bindings and remaining document content must match. Any mismatch rolls back the entire update, including history, snapshot, Assembly clock and revisions.

## Reproducible local evidence

`TestGuardedNativeJoinGatewayMySQL` uses the production JSON gateway, gRPC service and InnoDB store and a loopback S3 response behind the normal bucket client. Provide `AC17_JOIN_TEST_MYSQL_DSN` for a disposable loopback database with a free card ID 156. The ordinary Wizard fixtures bind that card, the database's M size ID 4, and an unrelated pocketing L at ID 5. Both R12 and R2000 tests require successful Apply transport, a fresh card GET and independent native reread. They compare the untouched L pattern and pocket pressing operation, reject unowned label POSEs and a versionless semantic sidecar, and use an InnoDB trigger to prove that the post-write check rolls back an already reached child INSERT. A duplicate request with the previous lock returns conflict.
