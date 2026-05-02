# Release Checklist

Use this checklist before publishing a preview release.

## Version

- [ ] Set `pkg/pgsync.Version` to the release tag.
- [ ] Update `README.md` with the release version.
- [ ] Update `CHANGELOG.md`.
- [ ] Confirm the tag name matches the version, for example `v0.1.0-preview.3`.

## Validation

- [ ] Run `./scripts/harness-check.sh`.
- [ ] Run `./scripts/harness-check.sh --all` before a public release.
- [ ] Run `docker compose config --quiet`.
- [ ] Run `docker compose -f test/e2e/docker-compose.yml config --quiet`.
- [ ] Build the Docker image with `docker build -t postgres-sync-go:local .`.
- [ ] Smoke-test `/v1/health` and one `/v1/shape` request from the Compose stack.

## Release Artifacts

- [ ] Build binaries for supported platforms.
- [ ] Build and tag the Docker image.
- [ ] Generate an SBOM for the exact release artifacts.
- [ ] Generate third-party dependency license notices.
- [ ] Attach checksums for binary artifacts.

## Public Metadata

- [ ] Confirm `README.md`, `SECURITY.md`, `NOTICE`, and `LICENSE` render correctly.
- [ ] Confirm GitHub issue templates and PR template render correctly.
- [ ] Confirm no private paths, secrets, generated artifacts, or ignored runtime files are included.
- [ ] Push the release tag.
- [ ] Publish release notes from `CHANGELOG.md`.
