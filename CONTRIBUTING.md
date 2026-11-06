# Contributing to whoas

Thanks for helping! Adapters for new CLIs are the most welcome contribution.
For anything larger, open an issue (or a [Discussion](https://github.com/S-kkipie/whoas/discussions)
for questions) first so we can agree on the approach before you write code.

Security problems: please don't open an issue, see [SECURITY.md](SECURITY.md).

## Development setup

You need Go at least as new as the `go` line in [`go.mod`](go.mod). No other
tools, services or accounts: the tests never touch the network, your keychain
or your real CLIs.

```sh
git clone https://github.com/S-kkipie/whoas && cd whoas
go build ./cmd/whoas          # ./whoas (git-ignored)
go test ./...                 # unit + end-to-end
go test -race ./...           # what CI runs
GOOS=windows go vet ./...     # the Windows build (whoas.exe, the WSL keystore helper) must compile
gofmt -l .                    # must print nothing
```

End-to-end tests live in [`tests/`](tests): each `tests/testdata/*.txtar` is a
[testscript](https://pkg.go.dev/github.com/rogpeppe/go-internal/testscript)
that runs the real `whoas` binary against fake CLIs (`tests/fakecli`) and a
fake MCP server (`tests/fakemcp`). Run one with
`go test ./tests -run TestScripts/shims`.

## Project layout

| Path | What lives there |
|---|---|
| `cmd/whoas` | `main` — just calls into `internal/app` |
| `internal/app` | Cobra commands: add, import, run, ls, profile, trust, shims, doctor, mcp… |
| `internal/adapter` | Adapter schema, validation and registry; built-in adapters in `builtin/*.toml` |
| `internal/supervisor` | Starts the real CLI: env, signals, exit codes, seal-on-write watcher |
| `internal/overlay` | The per-identity config overlay (symlinks + decrypted auth files) |
| `internal/mcpproxy` | `whoas mcp proxy`: stdio MCP server that forwards with the identity's headers |
| `internal/vault`, `keystore`, `agent` | Sealed sessions, the master key backends, the key agent |
| `internal/resolve`, `project`, `trust`, `config` | Which identity applies: profiles, `.whoas.yaml`, trust |
| `tests/` | End-to-end testscripts, fake CLIs and the fake MCP server |

## Adding a CLI

1. Write the adapter as described in [README → Adding a CLI](README.md#adding-a-cli)
   and try it as a user adapter in `~/.config/whoas/adapters/` against the real CLI
   (check `whoas <cli>@alias <whoami-like command>` returns the right account).
2. Move it to `internal/adapter/builtin/<name>.toml`. Built-ins are embedded and
   validated at load time.
3. Extend the tests that pin the built-ins in
   [`internal/adapter/adapter_test.go`](internal/adapter/adapter_test.go)
   (`TestBuiltinsLoad`, `TestDirOverlayBuiltins`, `TestTokenBuiltins`,
   `TestMCPBuiltins`): assert the env vars, flags and auth files you declared.
4. Update the README: the [Supported CLIs](README.md#supported-clis) table for a
   `dir-overlay` adapter, or the API-token list and count for `env-token`.

Unsure how a CLI stores its login? Open a
[new CLI request](https://github.com/S-kkipie/whoas/issues/new?template=new_cli.yml)
with what you know.

## Tests

- Unit tests sit next to the code (`foo.go` → `foo_test.go`); end-to-end tests go in `tests/`.
- A bug fix comes with a test that fails without it.
- **Never commit secrets.** Tests use obviously fake values (`tok-1`,
  `faketok`); never paste a real token, `hosts.yml` or `whoas doctor`
  output with account data into a test, fixture, issue or commit.

## Commits and pull requests

- Commit subjects follow [Conventional Commits](https://www.conventionalcommits.org):
  `feat:`, `fix:`, `docs:`, `test:`, `build:`, `chore:` with an optional scope
  (`feat(adapter): add doctl`). Add `!` for a breaking change. The release
  changelog is generated from these subjects.
- Keep a PR to one change. Before opening it:
  - [ ] `go test -race ./...` passes
  - [ ] `GOOS=windows go vet ./...` passes and `gofmt -l .` is empty
  - [ ] new behaviour has tests; user-visible changes are in the README
  - [ ] no secrets, real tokens or real account names anywhere in the diff

## Releases (maintainer)

Push a `v*` tag with hand-written notes in `.github/release-notes/<tag>.md`;
[`release.yml`](.github/workflows/release.yml) runs the tests and GoReleaser
([`.goreleaser.yaml`](.goreleaser.yaml)), which builds the archives,
`checksums.txt` and the changelog. Dry run:
`goreleaser release --snapshot --clean --skip=publish`.
