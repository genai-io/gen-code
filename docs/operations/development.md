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
