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

Provider-specific formats, HTTP, authentication, and replay validation stay in
Artex's `chatgptllm` and `chatgptauth` packages. Local regression tests cover
accumulation, projection, transcript persistence, and summary retry behavior.
