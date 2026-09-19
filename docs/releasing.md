# Releasing

This is the maintainer procedure for a Public Beta release. Commands below are
instructions, not evidence that a release or live smoke test has been performed.
Use [verification](verification.md) for the current record and
[security](security.md) for the data boundary. Publishing and live tests require
separate authorization.

## Finish and review versioned inputs

Use Go **1.26.7**, pinned in `.go-version`. All runtime builds must keep
`CGO_ENABLED=0`, `-trimpath`, and `-buildvcs=false`. Make selects the pinned
toolchain; direct Go commands must select it explicitly.

For a new version, review these locations together:

| Location | Versioned information |
| --- | --- |
| `cmd/jev-preflight/main.go` | Runtime version reported by `version` |
| `cmd/jev-preflight/main_test.go` | Expected runtime version output |
| `.claude-plugin/plugin.json` | Installed plugin version |
| `.claude-plugin/marketplace.json` | Plugin version, versioned release archive URL, and final SHA-256 pin |
| `Makefile` | Default `VERSION` |
| `.github/workflows/ci.yml` | Package-job version, archive paths, and native version assertion |
| README and release instructions | User-visible version and examples |

The tag format is `vX.Y.Z`; manifest versions omit the leading `v`. The archive URL
must use the same tag and filename. The packager validates plugin name, versions,
and the expected repository release URL. The release workflow also executes the
native unpacked launcher and checks its reported version.

Finish code, policy, launcher, and README changes before computing the digest.
README is packaged, so editing it changes archive bytes. The marketplace catalog
and `docs/` are source-only and are excluded from the archive. Updating only a
catalog pin or verification document therefore does not change the archive.

## Prepare and pin the archive

```sh
GOTOOLCHAIN=go1.26.7 make check
GOTOOLCHAIN=go1.26.7 make test-race
GOTOOLCHAIN=go1.26.7 make prepare-pin VERSION=v0.1.0
```

`prepare-pin` builds all six targets, assembles and unpacks the archive, and prints
its actual SHA-256. It bypasses checking the existing pin, but retains version,
URL, binary architecture, and layout checks. It never changes source files.

Review the generated digest and explicitly update `source.sha256` in
`.claude-plugin/marketplace.json` to that 64-character lowercase value. Do not use
a guessed value or placeholder. Then package twice in normal strict mode:

```sh
GOTOOLCHAIN=go1.26.7 make package VERSION=v0.1.0
GOTOOLCHAIN=go1.26.7 make package VERSION=v0.1.0
git diff --check
```

Normal packaging rejects a missing, invalid, or mismatched marketplace pin before
replacing the archive. It never rewrites the catalog. Compare both runs' printed
digests; verify that the marketplace pin, ZIP digest, and checksum sidecar agree.
On Linux/Git Bash, the sidecar can be checked with:

```sh
(cd dist && sha256sum -c jev-preflight-plugin-v0.1.0.zip.sha256)
```

On macOS, use `shasum -a 256 -c` in the same directory. These are maintainer
verification tools, not plugin runtime dependencies.

The outputs stay under ignored `dist/`:

```text
dist/jev-preflight-plugin-v0.1.0.zip
dist/jev-preflight-plugin-v0.1.0.zip.sha256
dist/unpacked/jev-preflight-plugin-v0.1.0/
```

## Deterministic contents and verification

The ZIP has no enclosing top-level directory. It contains exactly twelve files:

- `.claude-plugin/plugin.json`, `hooks/hooks.json`, and `policies/default.json`.
- `scripts/run`, `README.md`, and `LICENSE`.
- Six binaries at `scripts/runtime/<os>-<arch>/jev-preflight[.exe]`.

The marketplace catalog and `docs/` must remain absent. The packager rejects
unexpected inputs and symlinks, checks binary format/architecture, extracts the
ZIP, and compares every extracted file with its input. Entries are sorted by
path, use a fixed UTC timestamp and permissions, and use Go's Deflate compressor.
The launcher and binaries have executable archive modes. `.gitattributes`
normalizes packaged text to LF.

Regression tests compare ZIP bytes across source roots, source timestamps and
permissions, locale/timezone settings, and explicit pin updates. Use the pinned
toolchain for both builds and packaging. The Ubuntu CI package job must also
match the committed pin; matching builds on one local host alone do not establish
cross-host reproducibility.

The marketplace `source.sha256` lets Claude Code verify the downloaded ZIP during
installation. The `.zip.sha256` asset supports manual verification. Neither is a
signature or protection against a compromised publisher updating both values.

Before release, require green checks for the exact revision and validate both
source catalog and unpacked plugin with Claude Code 2.1.257 or newer:

```sh
claude --version
make plugin-validate
make plugin-validate PLUGIN_DIR=dist/unpacked/jev-preflight-plugin-v0.1.0
```

The Make targets skip an absent or older CLI explicitly. On a supported version,
they run `claude plugin validate <directory> --strict`. A skipped check remains
pending. Confirm the current TypeSafe data-handling and commercial terms before
publishing; see [security](security.md). Section 2.3(f) of TypeSafe's
[Master Customer Agreement](https://typesafe.ai/legal/mca) restricts publication
of service benchmarks and performance information. Do not publish self-measured
latency, cost benchmarks, speed claims, or model comparisons without written
permission.

## Release workflow

After review and authorization, the release tag must point to a reviewed
default-branch commit containing the intended workflow and final catalog pin.
A pushed `v*` tag triggers `.github/workflows/release.yml`:

1. Ubuntu, macOS, and Windows run the test suite.
2. The Ubuntu build job runs quality/race checks, builds all six static targets,
   and performs strict packaging against the committed marketplace pin. A pin
   mismatch fails before artifact upload or publication.
3. It verifies the sidecar and native launcher version, then validates the source
   and unpacked plugin only if a supported Claude CLI is available. It does not
   install or upgrade that CLI.
4. A separate publish job downloads the checked artifacts, verifies the sidecar
   again, and creates a GitHub prerelease for the already-existing tag. It uses
   the Public Beta title and does not mark the release as latest.

The workflow's optional CLI check does not replace the maintainer's supported-CLI
release gate. Do not infer a live hook/API result from successful tests, builds,
or archive verification.

## Post-release marketplace smoke test

Run this manual procedure only after the versioned release assets exist and with
explicit opt-in for any model/API use. Use a disposable Git repository containing
synthetic code, not private project data.

1. Confirm Claude Code is at least 2.1.257. Add the marketplace and install the
   released plugin:

   ```sh
   claude plugin marketplace add muse0509/jev-preflight
   claude plugin install jev-preflight@jev-preflight
   claude plugin enable jev-preflight@jev-preflight
   ```

2. Confirm the installed version and that installation accepted the pinned
   archive. Enabling permits external-service use when a key is configured.
3. For an explicitly authorized no-key hook check, leave both key environment
   variables and the sensitive plugin option unset. Submit a synthetic edit
   prompt, make a meaningful code change, and confirm Stop allows completion
   without a TypeSafe request. This still invokes a Claude model session.
4. For a separately authorized API smoke test, set the sensitive plugin option
   through Claude Code or provide the key securely through the environment.
   Never paste it into a command argument, repository file, issue, or log.
   Submit a new synthetic edit prompt so a fresh baseline is created.
5. Observe the Stop outcome. A qualifying score should request one reinspection;
   its following Stop must finish without another continuation. A below-threshold
   result is valid, but does not verify the full Jev-triggered continuation path.
   Record those results separately in [verification](verification.md).

Jev receives selected/redacted paths and the turn diff, plus the fixed eight
questions. The key is used only in the HTTPS Authorization header, not the state
payload. Hook prompt, assistant, transcript, and cron bodies are not sent to Jev.
Model and API calls may incur provider charges. Keep the default threshold marked
uncalibrated, and do not turn a single smoke result into an accuracy claim.
