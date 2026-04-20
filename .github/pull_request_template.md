## Summary

<!-- What changed and why? Keep this short and specific. -->

## Validation

<!-- List the commands you ran. If something was not run, say why. -->

- [ ] `./scripts/harness-check.sh`
- [ ] Targeted E2E/lifecycle/shadow-client check, if relevant:

## Risk

<!-- Call out compatibility, storage, replication, or deployment risks. -->

- [ ] Public Go API is unchanged, or the API change is intentional and documented.
- [ ] HTTP protocol behavior is unchanged, or compatibility impact is documented.
- [ ] Docs/README/quality notes were updated when behavior changed.
- [ ] No secrets, private paths, generated artifacts, or local runtime files are included.
