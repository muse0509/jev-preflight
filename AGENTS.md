# Repository invariants

- Use Go standard library only; one `jev-preflight` runtime binary.
- Tests must never mutate the real repository, index, refs, objects, HOME, or global Git config.
- Put temporary repositories and dynamic test data in `t.TempDir()` and isolate Git configuration.
- Ordinary API/network tests use fake servers only. Live TypeSafe validation requires explicit user opt-in, the `jev_live` build tag, `JEV_PREFLIGHT_LIVE_TEST=1`, and an inherited `TYPESAFE_API_KEY`; send only the fixed synthetic fixture, never repository content.
- Live validation must count requests, keep credentials/headers/bodies out of output and files, and stay out of CI and packaging. Do not start Claude sessions or upgrade its CLI without the user's authorization.
- Never persist or log secrets, prompts, assistant output, patches, or API bodies. Private Git snapshots are temporary and must be cleaned up.
- Generated output belongs only in ignored `dist/`, `coverage/`, or `.tmp/`.
- `make check` is the local quality gate. Also run race tests and cross-builds for runtime changes.
- Never commit binaries, coverage, archives, temporary state, or credentials.
- Keep comments, documentation, and user-facing strings concise and in English.
