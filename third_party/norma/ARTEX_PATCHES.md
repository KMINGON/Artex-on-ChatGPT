# Norma runtime snapshot

This directory contains the complete Go module source of
[`github.com/Autumn-27/norma`](https://github.com/Autumn-27/norma) **v0.4.3**,
retrieved from the Go module cache. Upstream commit:
`320ae32989da97dd91469bb89164cbfe87d83d5d` (tag `v0.4.3`).

Original module checksum:
`h1:DnmOhJPL6OZSMnCVYO5Ujh4yVINuDarEickVi9jx1hI=`.

The upstream README, module files, examples, and tests are retained. The source
distribution contained no LICENSE or NOTICE file; this file does not supply a
new license or change the upstream author's rights.

Artex uses a local module replacement for these limited runtime extensions:

- `llm.Message.ProviderData` persists opaque provider replay state in normal
  transcript messages. It is not user-facing text.
- `llm.SEMessageFinal` supplies an authoritative completed message after live
  deltas. The accumulator owns a deep copy while continuing to track usage and
  stop reason. Providers still emit normal tool events for execution.
- History projections preserve unchanged provider state and clear it when
  content changes, preventing removed tool calls from returning through raw
  replay data.
- Summary generation respects an error's `NoFallback() bool` marker instead of
  automatically repeating an ineligible model request.
- Compaction keeps complete tool exchanges across the retained-tail boundary.
  Its gate also considers current visible messages and system/tool-schema
  overhead; an inserted boundary invalidates the previous usage estimate.
- A cancelled tool batch retains its original context. Waiting for a concurrency
  slot is cancellable, and queued handlers cannot inherit a settlement context.
- The final tool-output cap trusts only results captured by the same invocation,
  not marker strings embedded in untrusted output. Monitor receives BashEnv.
- Stdio MCP processes use an isolated process group on Unix. Close and failed
  initialization terminate that group; Windows uses taskkill /T (not runtime
  tested here).
- Exhausted output continuation returns `output_limit`, not `completed`.
  Incomplete tool input is rejected without invoking its handler.
- Transcript read/write failures return `storage_error`; storage errors prohibit
  whole-run replay and provider fallback. Session.Close also reports the first
  persistence failure. ARTEX's Chat/Main/Worker callers now check Resume/Close.

Checkpoints now use `checkpoint_begin`, `checkpoint_part`, and
`checkpoint_commit` records with version 1, an ID, count, ordered indices, and a
SHA-256 digest of the complete working view. Parts use `checkpoint_message`
instead of `message`, so an old reader cannot mistake an incomplete part for a
normal message. File contents are synced, followed by the containing directory
on Unix. Windows directory durability still needs native verification.

Resume accepts legacy messages and complete legacy checkpoints. A recognized
incomplete new checkpoint is excluded while keeping the previous complete
checkpoint and subsequent ordinary messages. Recovery writes a synced temporary
file and atomically publishes `<source>.recovered`, preserving the source bytes;
subsequent reads/writes follow that file (up to 32 recovery generations). A torn
last JSON line is recoverable only inside a known pending checkpoint. Other
corruption, unknown record types/versions, or invalid committed digests stop
resume. A complete final JSON record missing its newline is copied before
appending so two records cannot concatenate. A legacy bare final boundary is recoverable; partially saved legacy
summary/tail sequences cannot be reliably identified without commit metadata.

Do not let old and new binaries write these files concurrently. Older readers
do not understand committed checkpoints or recovery files. Rollback requires
the preserved pre-upgrade transcript backup and matching connection bindings;
pointing an old binary at newly written history can restore an incorrect view.
Noa runtime/configuration and its existing seven failing baseline tests are
unchanged in this reliability patch.

Provider-specific formats, HTTP, authentication, and replay validation stay in
Artex's `chatgptllm` and `chatgptauth` packages. Local regression tests cover
accumulation, projection, transcript persistence, and summary retry behavior.
The reliability regressions also cover checkpoint interruption/durability
failures, preserved post-checkpoint tool results, source immutability, repeated
resume, cancelled queues, output limits, spill markers, MCP children, and
Monitor environment propagation. These tests use temporary files and local
fixtures, with no live model or production database access.
