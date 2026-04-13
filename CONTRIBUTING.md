# Contributing to dotkeeper

Contributions are welcome! Bug reports, feature requests, and pull requests all help.

## Contributor License Agreement (CLA)

By submitting a pull request, you agree to the following:

1. You license your contribution under the same license as this project (AGPL-3.0).
2. You grant the project maintainer a perpetual, worldwide, non-exclusive, royalty-free, irrevocable license to use, modify, sublicense, and relicense your contribution under any license, including proprietary licenses.
3. You confirm that you have the right to make this grant (i.e., the contribution is your original work, or you have the necessary rights from your employer or other rights holder).

This CLA ensures the maintainer can adapt the project's licensing as needed while keeping the open-source version available under AGPL-3.0.

## Development

### Building

```bash
make build
```

### Testing

```bash
make test
```

### Code style

- Follow standard Go conventions (`gofmt`, `go vet`)
- Keep commits focused — one logical change per commit
- Write clear commit messages

## Reporting issues

Open an issue on GitHub. Include:
- What you expected to happen
- What actually happened
- Your OS and Go version
- Relevant output from `dotkeeper status`
