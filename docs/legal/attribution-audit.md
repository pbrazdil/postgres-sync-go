# License And Attribution Audit

Date: 2026-05-02

This is an engineering audit for public-release readiness. It is not legal advice.

## Conclusion

- No license-incompatible source copying was identified.
- postgres-sync-go should be released under Apache License 2.0.
- Keep a root `NOTICE` with a compatibility attribution and non-affiliation statement.
- Keep third-party dependency licenses intact in source form. For binary/container releases, generate and ship a dependency notice bundle as part of the release process.

## Scope

Reviewed:

- postgres-sync-go source, docs, Docker, and test harness files outside ignored/generated artifacts.
- Optional comparison sync-service tree license files when available.
- Go modules currently used by `go list -deps -test ./...`.
- A mechanical long-line overlap scan between postgres-sync-go and the optional comparison tree when available.

Not reviewed:

- Full legal trademark clearance.
- Every transitive source file by hand.
- NPM, Elixir, or Docker image dependencies used only by the optional comparison harness.
- Production binary/container SBOM generation.

## Comparison Source License

The optional comparison sync-service tree used during this audit contains:

- root `LICENSE`: Apache License 2.0
- sync-service package `LICENSE`: Apache License 2.0
- no `NOTICE` file found under the checked comparison tree

Apache-2.0 is permissive and compatible with an Apache-2.0 postgres-sync-go release, assuming required notices and license text are preserved for any copied or derived material.

## Source-Copying Review

Observed evidence:

- postgres-sync-go is implemented in Go while the comparison sync-service implementation audited here is not Go.
- No comparison-source copyright headers were found in postgres-sync-go source.
- No source files from the comparison implementation are vendored into postgres-sync-go.
- The compatibility fixture test reads the local OpenAPI file from the ignored comparison tree instead of copying it into this repository.
- A mechanical exact-match scan for non-empty lines of at least 70 characters found no actionable copied source or configuration. Earlier generic sample database URLs have been renamed to use postgres-sync-go public naming.

Engineering conclusion: postgres-sync-go appears to be an independent, protocol-compatible rewrite rather than a source copy. No license-incompatible copied source was identified by this audit.

## Attribution Decision

Use these release files:

- `LICENSE`: Apache License 2.0 for postgres-sync-go.
- `NOTICE`: postgres-sync-go copyright, compatibility attribution, and non-affiliation statement.

Recommended public wording:

> postgres-sync-go is an independent Go implementation of an Electric-compatible sync-service HTTP surface. ElectricSQL, Electric, and related marks belong to their respective owners. postgres-sync-go is not affiliated with or endorsed by those owners unless explicitly stated.

Avoid wording that implies endorsement, official status, or ownership of Electric names.

## Go Dependency License Summary

Current dependency license posture based on module license files in the local Go module cache:

| Module | Version | License signal |
| --- | --- | --- |
| `github.com/dustin/go-humanize` | `v1.0.1` | MIT-style |
| `github.com/google/uuid` | `v1.6.0` | BSD-3-Clause-style |
| `github.com/jackc/pgio` | `v1.0.0` | MIT |
| `github.com/jackc/pglogrepl` | `v0.0.0-20250509230407-a9884f6bd75a` | MIT |
| `github.com/jackc/pgpassfile` | `v1.0.0` | MIT |
| `github.com/jackc/pgservicefile` | `v0.0.0-20240606120523-5a60cdf6a761` | MIT |
| `github.com/jackc/pgx/v5` | `v5.7.6` | MIT |
| `github.com/jackc/puddle/v2` | `v2.2.2` | MIT |
| `github.com/mattn/go-isatty` | `v0.0.20` | MIT |
| `github.com/ncruces/go-strftime` | `v1.0.0` | MIT |
| `github.com/remyoudompheng/bigfft` | `v0.0.0-20230129092748-24d4a6f8daec` | BSD-3-Clause-style |
| `golang.org/x/crypto` | `v0.37.0` | BSD-3-Clause |
| `golang.org/x/sync` | `v0.19.0` | BSD-3-Clause |
| `golang.org/x/sys` | `v0.42.0` | BSD-3-Clause |
| `golang.org/x/text` | `v0.24.0` | BSD-3-Clause |
| `gopkg.in/yaml.v3` | `v3.0.1` | MIT and Apache-2.0, includes `NOTICE` |
| `modernc.org/libc` | `v1.70.0` | BSD-style, includes third-party license file |
| `modernc.org/mathutil` | `v1.7.1` | BSD-style |
| `modernc.org/memory` | `v1.11.0` | BSD-style plus Go/license files |
| `modernc.org/sqlite` | `v1.48.2` | BSD-style |

No GPL, LGPL, AGPL, SSPL, or other strong-copyleft Go runtime dependency was identified in this audit pass.

## Required Follow-Ups Before Binary/Container Release

- Generate a machine-readable SBOM and third-party notices from the exact release build.
- Include dependency license texts in binary/container release artifacts.
- Re-run this audit after changing dependencies or vendoring any code.
- Have counsel review trademark/public-positioning language before a broad public announcement.
