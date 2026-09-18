# Repository invariants

- Use Go standard library only; one `jev-preflight` runtime binary.
- Tests must never mutate the real repository, index, refs, objects, HOME, or global Git config.
- Put temporary repositories and dynamic test data in `t.TempDir()` and isolate Git configuration.
- API/network tests use fake servers only; never call TypeSafe from tests.
- Never persist or log secrets, prompts, assistant output, patches, or API bodies. Private Git snapshots are temporary and must be cleaned up.
- Generated output belongs only in ignored `dist/`, `coverage/`, or `.tmp/`.
- `make check` is the local quality gate. Also run race tests and cross-builds for runtime changes.
- Never commit binaries, coverage, archives, temporary state, or credentials.
- Keep comments, documentation, and user-facing strings concise and in English.
