<!--
Thanks for contributing! Please fill in the sections below. Delete any that don't apply.
-->

## Summary

<!-- What does this PR change, and why? One or two sentences is often enough. -->

## Type of change

- [ ] Bug fix (non-breaking)
- [ ] New feature (non-breaking)
- [ ] Breaking change (requires a major version bump)
- [ ] Documentation / CI / chore
- [ ] Refactor (no behaviour change)

## Linked issues

<!-- e.g. Fixes #123, Closes #456, Related to #789 -->

## Testing

<!-- Describe how you tested this. For bug fixes, a regression test is expected. For new features, tests exercising the happy path and edge cases are expected. -->

- [ ] `make build` passes
- [ ] `make test` passes
- [ ] `golangci-lint run --build-tags noassets ./...` clean
- [ ] For changes to `internal/stengine`: restarted the service locally and `dotkeeper status` still peers
- [ ] For user-visible changes: README / CONTRIBUTING updated if applicable

## Notes for reviewers

<!-- Anything non-obvious, or reviewer attention points. Optional. -->
