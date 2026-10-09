#!/usr/bin/env bash
# T28 v2 C2 — the one lock order of the edit-chain / bench writes (internal/store/design/locks.go):
# tech card → the pictures the transaction writes (by id) → the card's bench slots, all FOR UPDATE,
# BEFORE any shared read of those rows. Store tests must never be run locally (they hit the
# production DB), so this is a static check of the writers' bodies.
#
#   scripts/check-design-lock-order.sh        — exit 1 on a writer that breaks the order
set -euo pipefail
cd "$(dirname "$0")/.."
python3 - <<'PY'
import re, sys
D = 'internal/store/design/'
# writer: (file, func signature prefix, picture-lock token)
writers = [
    ('edit_chain.go', 'func (s *Store) editChainStep(', 'ORDER BY id FOR UPDATE'),
    ('layer.go', 'func (s *Store) FlattenEditLayer(', 'lockDesignPictures('),
    ('bench.go', 'func (s *Store) SetBenchSlot(', 'lockDesignPictures('),
]
# the first read or write of a locked row that must come after all three locks
after = ['pictureByID(', 'pictureByRequestKey(', 'setBenchSlotTx(', 'layerByID(', 'moveBenchSlots(']
bad = 0
for f, sig, piclock in writers:
    src = open(D + f).read()
    i = src.index(sig)
    j = src.index('\nfunc ', i + 1)
    body = src[src.index('txFunc(', i, j):j]
    def at(tok):
        k = body.find(tok)
        return k if k >= 0 else None
    card, pics, bench = at('lockDesignCard('), at(piclock), at('lockDesignBench(')
    first = min([k for k in (at(t) for t in after) if k is not None], default=None)
    ok = None not in (card, pics, bench) and card < pics < bench and (first is None or bench < first)
    print(('  ok  ' if ok else '  FAIL') + f' {f}: {sig[16:-1]} card={card} pictures={pics} bench={bench} first-read={first}')
    bad += not ok
print(f'design-lock-order: {len(writers) - bad} ok, {bad} failed')
sys.exit(1 if bad else 0)
PY
