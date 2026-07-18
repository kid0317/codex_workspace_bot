# S04 Delivery Hardening Design

## Goal

Make the user-selected CardKit full-entity update the formal S04 work-mode path, and make companion terminal delivery ownership deterministic and unit-testable so S04 can enter its delivery-closeout gate.

## Scope and decisions

- CardKit continues to update the original entity with one full `card_json` per update. Component-level CardKit text updates, stable component-operation UUID retry, and component-level L3b are removed from the S04 contract. A full-entity failure may use the existing same-message PATCH fallback.
- Companion real-message checks already demonstrate successful plain-text delivery. This iteration does not require new real empty, failed, or restart-abandoned messages because the user cannot validate them through Feishu now.
- The broad AT-00--AT-24 expansion is deferred. The new ownership boundary must, however, have focused deterministic unit tests.

## Design

`TerminalArbiter` is a per-batch, mutex-protected first-wins decision object. It exposes a single `Claim(reason)` operation and keeps the immutable winning reason. Process completion, timeout, cancellation, workflow-writer failure, and delivery failure all claim through it before externally visible cleanup.

`DeliverySlot` is owned by one channel worker. It has explicit `idle`, `marked`, `published`, and `done` phases. `Begin` records that the durable companion marker succeeded; `Publish` either returns a cancellable child context or reports the pre-existing cancellation latch; `CancelAndWait` latches cancellation before or after publishing and waits until `Finish`. It is the only companion-delivery control surface used by `/cancel`, `/stop`, and future `/new` cleanup.

The workflow dependency changes from a bare `*slog.Logger` call to a small error-returning writer interface. A JSONL adapter writes the existing structured metadata; a writer failure is converted to `companion_delivery_trace_incomplete`, cancels the slot, prevents any subsequent segment, and uses the existing batch-level failure transaction. The event includes `thread_id`, `turn_id`, and `at`; unavailable IDs are emitted as empty strings rather than fabricated values.

## Tests

Focused worker unit tests cover: first terminal reason wins; cancellation before publish sends zero segments and waits; cancellation after publish stops later segments; writer failure stops later segments and invokes batch failure; and normal successful delivery finalizes exactly once. Tests use deterministic channels/fakes, not real Feishu or sleeps beyond bounded test deadlines.

## Error handling

No companion segment is retried due to a workflow or database-finalize failure. A marker that was written but cannot be finalized is passed to the existing restart reconciliation policy. A terminal reason is never overwritten.

## Completion evidence

Run focused worker tests, full Go tests, vet, race checks, build, restart the just-built binary, verify health and all receiver connections, update S04/HLD/README/Story List, obtain an independent blocker-only review, and write the S04 retrospective/SOP delta before marking Delivered.
