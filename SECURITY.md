# Security Policy

## Supported Versions

`postgres-sync-go` is currently in public preview. Security fixes are targeted at the latest preview release only.

| Version | Supported |
| --- | --- |
| `v0.1.0-preview.2` | yes |

## Reporting A Vulnerability

Do not open a public issue for a suspected vulnerability.

Report security concerns privately to the maintainer with:

- affected version or commit
- deployment mode and relevant redacted config
- reproduction steps or proof of concept
- expected impact
- logs or request/response samples with secrets removed

## Security Notes

- Do not expose `SYNC_INSECURE=true` outside local development.
- Treat `SYNC_SECRET`, database URLs, and logs containing request URLs as sensitive.
- Use a Postgres role with only the permissions needed for the target deployment.
- Validate production deployments behind your normal TLS, auth, network, and observability controls.
- Preview releases are not yet recommended as a primary production replacement without workload-specific validation.
