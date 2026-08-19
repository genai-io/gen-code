# Development

## Common Commands

```bash
make build
make test
make lint
make format
```

## Sandbox-Friendly Test Command

Some environments block writes to the default Go build cache. Use a writable
cache when needed:

```bash
GOCACHE=/private/tmp/san-go-build-cache go test ./...
```

## Formatting

`make format` runs `gofmt` and `goimports`. Install `goimports` with:

```bash
make install-format-tools
```

## Linting

`make lint` runs four checks, and CI runs the same target:

| check | what it catches |
| --- | --- |
| `go vet` | the standard suspicious-construct set |
| `make format-check` | files `gofmt` / `goimports` would rewrite |
| `make lint-go` | the linters configured in `.golangci.yml` |
| `make lint-layers` | imports that violate the layer order |

`make lint-go` installs `golangci-lint` on first use; `make install-lint-tools`
does it ahead of time. The configured linters report defects rather than style,
so the tree is kept at zero findings — see the comment at the top of
`.golangci.yml` for the rule on adding one.

## Vulnerability Scanning

```bash
make vulncheck
```

`govulncheck` reports known vulnerabilities the code can actually reach — an
advisory in a package nothing calls is not reported — so a finding is a real
exposure and CI gates on it. Two sources need attention when it goes red:

- **A module.** Bump it; Dependabot opens a grouped `gomod` PR weekly and will
  usually get there first.
- **The Go standard library.** Bump the `go-version` pin in
  `.github/workflows/ci.yml` and `release.yml` to the current patch release of
  that line. Dependabot does not track the toolchain pin.

CI also runs the scan weekly, so an advisory published against a dependency
already in `go.mod` surfaces without waiting for the next pull request.

The standard-library half of the report reflects **your local toolchain**, not
the CI pin, so a contributor on a newer Go than CI can see findings CI does not
(and should still upgrade their own Go — the binary they build really is
affected). To reproduce exactly what CI sees:

```bash
GOTOOLCHAIN=go1.25.13 make vulncheck
```
