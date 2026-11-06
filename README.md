<h1 align="center">whoas</h1>

<p align="center">
  <b>Run any CLI as the account you meant to use.</b><br>
  Capture every OAuth login once. Switch per command, per project, or per AI agent —<br>
  without logging out, and without ever touching the CLI's own session.
</p>

<p align="center">
  <a href="https://github.com/S-kkipie/whoas/actions/workflows/ci.yml"><img alt="CI" src="https://github.com/S-kkipie/whoas/actions/workflows/ci.yml/badge.svg"></a>
  <img alt="Go version" src="https://img.shields.io/github/go-mod/go-version/S-kkipie/whoas">
  <a href="LICENSE"><img alt="License: MIT" src="https://img.shields.io/badge/license-MIT-blue.svg"></a>
  <img alt="Platforms" src="https://img.shields.io/badge/platforms-Linux%20%7C%20WSL%20%7C%20macOS-informational">
</p>

```console
$ whoas add gh work               # log in once, through gh's own browser flow
$ whoas add gh personal

$ whoas gh@work repo list         # the real gh, as "work"
$ whoas gh@personal repo list     # at the same time, in another terminal, as "personal"

$ cd ~/code/client && whoas gh pr create   # picks "work" from the project's .whoas.yaml
```

---

## Why

You have two GitHub accounts, three Vercel teams, a personal and a company GCP
login, a couple of Neon orgs. Every one of those CLIs keeps **one global session**.
Switching means `logout` → browser → `login`, every single time — and the day
you forget, you deploy to the wrong account.

AI agents make it worse: Claude Code, Codex & co. run `gh`, `gcloud` or `vercel`
unattended and inherit **whatever session happens to be active**.

whoas keeps each login as a named, encrypted **identity** and runs the real CLI
against it — one process at a time, side by side, never mutating the CLI's own
config.

## Features

- 🔐 **Encrypted, local, open source.** Sessions are sealed with [age](https://age-encryption.org);
  the master key lives in your OS keychain (or Windows Credential Manager from WSL).
- 🌐 **Real OAuth sessions, not just API tokens.** whoas captures what the CLI's
  own login writes, so browser logins, refresh tokens and SSO keep working.
- 🔁 **Refreshed tokens are kept.** When the CLI rotates a token, whoas re-seals
  it within ~200 ms — even if the process later crashes.
- 🧭 **Context-aware.** Explicit `gh@work`, a profile (`-p work`), a session
  variable, or a trusted per-project `.whoas.yaml`.
- 🤖 **Built for agents.** Shims make `gh`, `gcloud`, … resolve the identity
  themselves, so an agent that has never heard of whoas still uses the right account.
- 🧱 **Global session untouched.** Your `~/.config/gh/hosts.yml` (and friends)
  is never written. Non-auth state — history, settings — stays shared.
- ⚡ **Concurrent and crash-safe.** Same identity in many terminals shares one
  workspace; different identities never collide; crashed runs are recovered.
- 🧩 **Declarative adapters.** Add a CLI with a small TOML file — no code.

## Supported CLIs

| CLI | Isolation | Session files captured | Env vars removed (they'd override the identity) |
|---|---|---|---|
| `gh` | `GH_CONFIG_DIR` | `hosts.yml` | `GH_TOKEN`, `GITHUB_TOKEN`, `GH_ENTERPRISE_TOKEN`, `GITHUB_ENTERPRISE_TOKEN` |
| `gcloud` | `CLOUDSDK_CONFIG` | `credentials.db`, `access_tokens.db`, `legacy_credentials/`, `configurations/`, `active_config`, `application_default_credentials.json` | `CLOUDSDK_CORE_ACCOUNT`, `CLOUDSDK_AUTH_ACCESS_TOKEN_FILE`, `CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE` |
| `codex` | `CODEX_HOME` | `auth.json` | — |
| `neonctl` | `--config-dir` flag | `credentials.json` | `NEON_API_KEY` |
| `vercel` | `--global-config` flag | `auth.json`, `config.json` (keeps the selected team per identity) | `VERCEL_TOKEN` |
| `wrangler` | `XDG_CONFIG_HOME` (overlay of `~/.config`) | `.wrangler/config/` (every wrangler auth profile) | `CLOUDFLARE_API_TOKEN`, `CLOUDFLARE_API_KEY` |
| `railway` ⚗️ | `HOME` (overlay of `~`) | `.railway/config.json` | `RAILWAY_TOKEN`, `RAILWAY_API_TOKEN` |
| `docker` | `DOCKER_CONFIG` | `config.json` (inline `auths`; see [docker](#docker)) | — |

⚗️ **experimental:** works, with a known gap — commands started by `railway run`
also see the overlay `HOME`. The banner and `whoas providers` mark it.

**Root overlays** (`railway`: all of `~`; `wrangler`: all of `~/.config`): tools
that save a dotfile by writing a new file and renaming it — `git config --global`,
some shells' history — lose that change inside a root overlay; whoas warns when
it happens (`whoas: warning: railway@work: .gitconfig was replaced during the run; …`).

**wrangler:** a legacy `~/.wrangler` directory wins over `$XDG_CONFIG_HOME` in
wrangler and would bypass the overlay, so whoas refuses to add or run wrangler
identities while it exists. Move it once: `mv ~/.wrangler ~/.config/.wrangler`.
On macOS wrangler keeps its session under `~/Library/Preferences/.wrangler`, so
`whoas import wrangler` may find nothing there (`add` and runs still isolate).

### API-token CLIs

CLIs that authenticate with an API token instead of a login get an
**`env-token`** adapter: whoas stores the token in the vault and injects it as an
env var for that one process. Built in: `supabase`, `terraform` (HCP Terraform),
`stripe`, `openai`, `netlify`, `fly`/`flyctl`, `glab`, `doppler`, `snyk`,
`sentry-cli`, `pulumi`, `hcloud`, `vultr-cli`, `linode-cli`, `twilio`,
`upstash`, `vault`, `gemini`, `grok`, `cargo`, `brew` and 42 more (64 in all) —
`whoas providers --all` lists them all.

```console
$ whoas add stripe work            # asks for the key; typing is not echoed
hint: Create one at https://dashboard.stripe.com/apikeys
Stripe Secret Key (STRIPE_API_KEY):
saved stripe@work
$ export STRIPE_API_KEY=sk_test_…  # or take the one already in your shell
$ whoas import stripe personal
$ printf '%s\n' "$TOKEN" | whoas add supabase ci --stdin    # scripts / CI
```

Isolation holds when the CLI prefers that env var over its own stored login (and
over other variables it reads). Check each CLI once with a command that prints
the account (`whoas <cli>@work <whoami-like command>`) before relying on it.

You can [add your own](#adding-a-cli) adapter for any CLI.

| Platform | Status |
|---|---|
| Linux | ✅ Secret Service keychain, or passphrase fallback |
| WSL | ✅ master key in Windows Credential Manager via `whoas.exe` |
| macOS | ✅ Keychain |
| Native Windows | ❌ not yet — the Windows build only ships the keystore helper WSL uses |

> **Status:** early (M2). The core is complete and heavily tested; expect
> rough edges and breaking changes before 1.0.

## Install

Requires Go ≥ 1.25.

```sh
go install github.com/S-kkipie/whoas/cmd/whoas@latest
```

Make sure `$(go env GOPATH)/bin` is on your `PATH`, then check your setup:

```sh
whoas doctor
whoas providers     # which supported CLIs are installed
```

<details>
<summary><b>WSL: keep the master key in Windows Credential Manager</b></summary>

Build the Windows helper next to the Linux binary **before your first
`whoas add`**:

```sh
git clone https://github.com/S-kkipie/whoas && cd whoas
GOOS=windows go build -o "$(dirname "$(command -v whoas)")/whoas.exe" ./cmd/whoas
```

whoas remembers the keystore it picks the first time (in
`~/.local/share/whoas/keystore`). `WHOAS_KEYSTORE=os|wsl|passphrase` overrides it
for a single run.
</details>

<details>
<summary><b>From source</b></summary>

```sh
git clone https://github.com/S-kkipie/whoas && cd whoas
go build -o ~/.local/bin/whoas ./cmd/whoas
```
</details>

## Quick start

**1. Capture your accounts.** `add` runs the CLI's own login in a sandbox and
seals the result; `import` takes the session you're already logged into.

```console
$ whoas import gh personal        # the account gh is logged into right now
saved gh@personal (octocat)
$ whoas add gh work               # opens gh's login; pick the other account
hint: If your browser is signed in to another GitHub account, switch accounts there first.
...
saved gh@work (octo-corp)
$ whoas ls
IDENTITY     ACCOUNT    ACTIVE
gh@personal  octocat    -
gh@work      octo-corp  -
```

**2. Run as an identity.** Everything after `provider@alias` goes to the CLI untouched.

```sh
whoas gh@work pr list --state open
whoas gh@personal api user --jq .login
```

**3. Group identities into profiles.**

```sh
whoas profile set work gh@work gcloud@acme neonctl@acme
whoas profile set personal gh@personal gcloud@me
whoas profile default personal          # used when nothing else applies

whoas -p work gcloud projects list       # -p goes before the provider
```

**4. Pin a profile to a project.**

```sh
cd ~/code/client
echo 'profile: work' > .whoas.yaml
whoas trust                              # project files must be approved first
whoas gh repo view                       # → gh@work
whoas which gh
# gh@work  via project /home/you/code/client/.whoas.yaml (profile work)
```

**5. Make it transparent with shims** (optional, but this is where it shines):

```sh
whoas shims install
# add to your shell rc:
#   export PATH="$HOME/.local/share/whoas/shims:$PATH"

cd ~/code/client && gh repo view         # plain `gh`, still gh@work
```

## Choosing the identity

For each run, the **first source that is present decides** — there is no
fallthrough, so a present-but-incomplete context never silently picks
something else:

| # | Source | Example |
|---|---|---|
| 1 | Explicit alias | `whoas gh@work …` |
| 2 | `-p` flag | `whoas -p work gh …`, `whoas run -p work -- …` |
| 3 | `WHOAS_PROFILE` | `export WHOAS_PROFILE=work` |
| 4 | Nearest trusted `.whoas.yaml` (walking up from the cwd) | `profile: work` — a `gh: other` line overrides it for `gh` |
| 5 | Default profile | `whoas profile default personal` |

If the chosen source has no identity for that CLI, an explicit `whoas gh …`
fails with a clear error, while a **shim passes through** to the CLI's normal
global session (whoas never breaks a CLI you call directly).

`whoas which <provider>` shows what would be used and why. When stderr is a
terminal, each run prints a one-line banner such as `whoas: gh@work (octo-corp)`
(`WHOAS_BANNER=always|never` to force it).

### Project files

```yaml
# .whoas.yaml — commit it, it holds no secrets
profile: work
gcloud: acme-staging     # per-CLI override
```

A cloned repo is untrusted input, so `.whoas.yaml` is **ignored until you run
`whoas trust`** — approval is recorded by content hash, and any edit revokes it.
The format is a strict whitelist: only `profile` and `provider: alias` strings,
at most 64 KiB, no YAML anchors or aliases. It can never declare commands,
paths, env vars or adapters.

## AI agents

Start the agent through `whoas run`. Every CLI it launches — however deeply
nested — goes through the shims and resolves to that profile:

```sh
whoas run -p work -- claude
whoas run -p personal -- codex
```

`run` puts the shims dir first on `PATH`, sets `WHOAS_PROFILE`, and `exec`s the
command. The agent doesn't need any whoas-specific instructions.

Working across several repos? Leave out `-p` (`whoas run -- claude`, or just
keep the shims on your `PATH`). Each repository's trusted `.whoas.yaml` then
picks the account as the agent moves between them.

## How it works

```mermaid
flowchart LR
    A["whoas gh@work pr list"] --> B{"resolve<br/>identity"}
    B --> C[("vault<br/>age-encrypted<br/>gh/work.age")]
    C -- "decrypt<br/>(agent / keychain)" --> D["overlay<br/>symlinks to ~/.config/gh<br/>+ hosts.yml for work"]
    D --> E["real gh<br/>GH_CONFIG_DIR=overlay"]
    E -- "token refreshed" --> F["seal-on-write"]
    F --> C
```

1. **Overlay.** whoas mirrors the CLI's config dir into a private runtime dir
   (`$XDG_RUNTIME_DIR/whoas`, tmpfs on Linux; `$TMPDIR/whoas` on macOS): every
   entry is a symlink to your real one, **except the auth files**, which are
   decrypted from the vault. History, settings and caches stay shared.
2. **Run.** The real binary starts with its config pointed at the overlay
   (env var or flag, per adapter) and with identity-overriding env vars such as
   `GH_TOKEN` removed. Arguments, stdin/stdout/stderr and the TTY pass through.
3. **Seal-on-write.** An fsnotify watcher re-encrypts the auth files shortly
   after the CLI rewrites them, so refreshed tokens survive crashes.
4. **Share and clean up.** Processes using the same identity share one overlay,
   coordinated with a file lock and a holder list. When the last one exits, the
   final state is sealed and the overlay is deleted. Overlays left by a crash
   are recovered by the next run or by `whoas doctor`.

Sealing only needs the vault's **public** key, so saving a refreshed token
never requires unlocking anything.

### The master key

The vault's private key (an age X25519 identity) is read from, in order:

1. `WHOAS_MASTER_KEY` — for CI and containers.
2. `WHOAS_KEYSTORE=os|wsl|passphrase` — force a backend.
3. The backend remembered when the vault was created.
4. Auto-detect: WSL bridge (`whoas.exe` → Windows Credential Manager), then the
   OS keychain (macOS Keychain / Linux Secret Service), then a passphrase-encrypted
   file (`WHOAS_PASSPHRASE` or a `/dev/tty` prompt).

When reading the key is slow or interactive (WSL bridge, passphrase), whoas
starts **`whoas agent`**, which caches it in locked memory (memguard) behind a
0600 unix socket. Clients send ciphertext and get plaintext back — the key never
leaves the agent. The passphrase cache expires after 8 h idle; `whoas lock`
drops it, `whoas unlock` loads it.

## Commands

| Command | What it does |
|---|---|
| `whoas <provider>[@alias] [args…]` | Run the CLI as an identity (args passed verbatim) |
| `whoas add <provider> <alias> [--stdin]` | Log in through the CLI (or enter its API token) and save it |
| `whoas import <provider> <alias>` | Save the CLI's current session, or the API token in your environment |
| `whoas ls [provider]` | List identities, their account and live processes |
| `whoas rm <provider>@<alias>` | Delete an identity (refused while in use) |
| `whoas which <provider> [-p profile]` | Show which identity would be used, and why |
| `whoas profile set\|ls\|show\|default` | Manage profiles |
| `whoas trust [file]` / `untrust [file]` | Approve or revoke a `.whoas.yaml` |
| `whoas run [-p profile] -- <cmd…>` | Run any command with shims and a profile for nested CLIs |
| `whoas shims install\|uninstall\|ls` | Manage shims (only ever touches whoas's own links) |
| `whoas agent` / `unlock` / `lock` | Key agent control |
| `whoas doctor` | Check setup; recover overlays and captures left by crashed runs |
| `whoas providers [--all]` | List the adapters of installed CLIs (`--all`: every adapter) |
| `whoas completion <shell>` | Shell completion script |

### Exit codes and signals

| Code | Meaning |
|---|---|
| CLI's own | passed through unchanged |
| `125` | whoas itself failed (unknown identity, untrusted project file, locked vault, …) |
| `126` | the CLI was found but could not be executed |
| `127` | the CLI is not on `PATH` |
| `2` | usage error (e.g. a dangling `-p`) |
| `128+n` | the CLI died from signal *n* (HUP/INT/TERM are re-raised as real signals) |

SIGTERM and SIGHUP are always forwarded to the CLI. SIGINT and SIGQUIT are
forwarded only when whoas is not in the terminal's foreground process group
(agents, CI) — in a terminal, Ctrl-C already reaches the CLI. Signals ignored by
whoas's parent (e.g. under `nohup`) stay ignored.

## Adding a CLI

Drop a TOML file in `~/.config/whoas/adapters/`:

```toml
# ~/.config/whoas/adapters/mycli.toml
name      = "mycli"
bin       = "mycli"
kind      = "dir-overlay"
unset_env = ["MYCLI_TOKEN"]          # env vars that would override the identity

[dir]
global     = "{xdg_config}/mycli"    # the CLI's real config dir: {home} {xdg_config} {xdg_data}
inject     = { env = { MYCLI_CONFIG_DIR = "{dir}" } }   # or: flag = ["--config-dir", "{dir}"]
auth_files = ["credentials.json"]    # files or dirs that hold the session

[login]
cmd  = ["mycli", "login"]
hint = "Pick the right account in the browser."   # optional

[whoami]                             # optional: shows the account in `whoas ls`
cmd  = ["mycli", "whoami", "--json"]
json = "email"                       # optional gjson path into stdout
```

Optional `[dir]` keys: `helper_keys = ["credsStore"]` lists top-level JSON keys of an
auth file that delegate credentials to external helpers (whoas warns about them);
`conflicts = ["{home}/.mycli"]` lists paths that make the CLI ignore the injected
dir (whoas refuses to add or run while one exists).
`experimental = true` (top level) marks an adapter with known gaps.

**A whole root as the overlay.** If the CLI only honours `$HOME` or `$XDG_*`, set
`global` to exactly `{home}`, `{xdg_config}` or `{xdg_data}` and inject the
matching variable — `HOME`, `XDG_CONFIG_HOME` or `XDG_DATA_HOME` — as `"{dir}"`.
Changes made by renaming a new file over an existing entry stay in the overlay
and are lost (whoas warns), so prefer a sub-directory overlay when the CLI allows it.

**API-token CLIs** use `kind = "env-token"` and declare their env vars instead of a `[dir]`:

```toml
name = "mycli"
bin  = "mycli"
kind = "env-token"

[[secret]]
env   = "MYCLI_TOKEN"
label = "MyCLI API Token"            # shown in the hidden prompt

[[secret]]
env      = "MYCLI_HOST"
label    = "MyCLI Host"
plain    = true                      # not secret: prompt with echo
optional = true

[login]
hint = "Create one at https://example.com/tokens"   # optional; no login cmd
```

Rules, enforced on load: unknown keys are errors; `bin` is a bare command name;
every `inject.env` value contains `{dir}`; `global` starts with `{home}/`,
`{xdg_config}/` or `{xdg_data}/` (or is exactly one of those roots, with its
variable injected); `auth_files` are clean relative paths; an `env-token` adapter
has at least one non-optional `[[secret]]` (never `HOME` or `XDG_*_HOME`) and no
`[dir]` or `login.cmd`; no two adapters share a `bin`. A user adapter with the
same `name` or `bin` replaces that built-in.

> Adapters are data, not scripts — but an adapter decides which binary runs, so
> only install adapters you trust. Project files can never declare one.

### docker

whoas isolates the credentials docker keeps **inline** in `config.json`
(`auths`). Registries listed under `credsStore` / `credHelpers` delegate to an
external helper (a keychain, `gcloud`, `ecr-login`…) whose store is global;
whoas warns when an identity contains them. Anything docker writes during a run
is captured, so you can add a registry to an identity with
`whoas docker@work login ghcr.io`. Because `config.json` is per identity, it also
carries docker's `currentContext`, proxies and plugin settings per identity (like
vercel's selected team).

## Security

**whoas protects against**

- Running against the wrong account — explicit resolution, banner, `which`, and
  the trust gate for project files.
- Sessions leaking at rest through backups, dotfile repos, cloud sync or a lost
  disk — the vault is encrypted; plaintext exists only in the runtime dir while
  a CLI is using it.
- whoas leaking secrets — `ls`, `doctor` and errors never print them; tokens are
  injected through files and env, never argv.
- Malicious repositories — `.whoas.yaml` needs `whoas trust` (by hash) and is a
  strict whitelist; a hostile file (directory, FIFO, huge, YAML bomb) is ignored
  with a warning by shims.
- Other local users — 0700/0600 permissions, plus a peer-uid check on the agent
  socket on Linux and macOS.

**whoas does not protect against**

- Malware running as your own user. It can read live overlays,
  `/proc/<pid>/environ`, or ask the agent to decrypt — the same boundary as
  aws-vault and the 1Password CLI.
- The CLI's own child processes — they inherit the identity by design.
- `WHOAS_MASTER_KEY` / `WHOAS_PASSPHRASE` set in the environment (CI,
  containers): every process whoas starts inherits them. On workstations, prefer
  the OS keychain or the agent.
- A compromised CLI binary.

Found a vulnerability? Please open a [private security advisory](https://github.com/S-kkipie/whoas/security/advisories/new)
instead of a public issue.

## Configuration reference

<details>
<summary><b>Environment variables</b></summary>

| Variable | Purpose |
|---|---|
| `WHOAS_PROFILE` | Profile for this session (resolution step 3); set by `whoas run -p` |
| `WHOAS_KEYSTORE` | Force the key backend: `os`, `wsl`, `passphrase` |
| `WHOAS_MASTER_KEY` | The age identity itself (CI/containers; read-only backend) |
| `WHOAS_PASSPHRASE` | Passphrase for the passphrase backend (non-interactive) |
| `WHOAS_WIN_EXE` | Path to `whoas.exe` for the WSL bridge |
| `WHOAS_BANNER` | `always` / `never` show the identity banner |
| `WHOAS_CONFIG_DIR` | Override `~/.config/whoas` |
| `WHOAS_DATA_DIR` | Override `~/.local/share/whoas` |
| `WHOAS_RUNTIME_DIR` | Override the runtime dir |
</details>

<details>
<summary><b>Files</b></summary>

| Path | Contents |
|---|---|
| `~/.config/whoas/config.yaml` | Profiles and default profile (0600, no secrets) |
| `~/.config/whoas/adapters/*.toml` | Your adapters |
| `~/.local/share/whoas/vault/<provider>/<alias>.age` | Sealed sessions (0600) |
| `~/.local/share/whoas/vault/<provider>/<alias>.meta.json` | Account label and dates (no secrets) |
| `~/.local/share/whoas/recipient` | Vault public key |
| `~/.local/share/whoas/keystore` | Remembered key backend |
| `~/.local/share/whoas/master.age` | Master key, passphrase-encrypted (passphrase backend only) |
| `~/.local/share/whoas/trust.json` | Approved project files (path → hash) |
| `~/.local/share/whoas/shims/` | Shims |
| `$XDG_RUNTIME_DIR/whoas/` | Live overlays, locks, agent socket (0700; falls back to `~/.cache/whoas/run`) |
</details>

## How it compares

| | whoas | 1Password Shell Plugins | aws-vault | `gh auth switch` | direnv |
|---|---|---|---|---|---|
| Many CLIs | ✅ adapters | ✅ | AWS only | gh only | any (env only) |
| Browser/OAuth sessions | ✅ | API tokens | AWS SSO | ✅ | ❌ |
| Concurrent identities | ✅ | ✅ | ✅ | ❌ global switch | ✅ |
| Per-project identity | ✅ trusted file | ✅ per directory | ❌ | ❌ | ✅ |
| Works for unattended agents | ✅ | desktop app unlock or a service account | ✅ | — | ✅ |
| Secrets encrypted at rest | ✅ age + keychain | ✅ | ✅ | keyring / file | ❌ plaintext |
| Free, open source, local | ✅ | requires subscription | ✅ | ✅ | ✅ |

## FAQ

<details>
<summary><b><code>whoas add</code> saved the same account twice</b></summary>

The browser reused the account it was already signed in to. whoas warns when
two identities report the same account. Remove it (`whoas rm gh@work`), switch
accounts in the browser (or use a private window), and `add` again.
</details>

<details>
<summary><b>"master key not found"</b></summary>

whoas is looking in a different keystore than the one that holds the key. Run
`whoas doctor` to see which backend is in use. Point it at the right one with
`WHOAS_KEYSTORE=os|wsl|passphrase`. To change it permanently, edit
`~/.local/share/whoas/keystore`.
</details>

<details>
<summary><b>"identity busy" / an identity looks stuck</b></summary>

Another process held the identity's lock for more than 10 s. If a run was
killed, `whoas doctor` recovers its overlay. `whoas ls` shows how many
processes are using each identity.
</details>

<details>
<summary><b>Does whoas send my tokens anywhere?</b></summary>

No. whoas has no network code: it only reads and writes local files and runs
the CLIs you ask it to run.
</details>

<details>
<summary><b>What happens if I run the CLI directly, without whoas?</b></summary>

Exactly what happened before installing whoas: the CLI uses its own global
session, which whoas never modifies. With shims installed, a direct call picks
an identity only when a profile, `WHOAS_PROFILE` or a trusted `.whoas.yaml`
applies. Otherwise it passes straight through.
</details>

## Roadmap

- ✅ **M2:** `vercel`, `wrangler`, `railway`, `docker`, and `env-token` adapters for
  API-token CLIs.
- **M3:** Claude Code + per-MCP-server accounts (compose one `.credentials.json`
  from several identities).
- **Later:** Claude Code on macOS (Keychain), native Windows, git identities
  (SSH keys, commit email), a local audit log.

Design docs: [`whoas`](docs/superpowers/specs/2026-11-05-whoas-design.md) and
[M2](docs/superpowers/specs/2026-11-06-whoas-m2-design.md).

## Development

```sh
go test ./...              # unit + end-to-end
go test -race ./...        # what CI runs
```

End-to-end tests ([`tests/`](tests)) build the real `whoas` binary and drive it
with [testscript](https://pkg.go.dev/github.com/rogpeppe/go-internal/testscript)
against a fake CLI that behaves like an OAuth CLI: it logs in, rotates tokens,
traps signals and fails on demand. They cover concurrency, crash recovery,
signals, exit codes, trust, shims and the guarantee that the global session is
never touched. Real-keyring round trips run in CI on Linux, macOS and Windows.

Contributions are welcome — especially adapters for new CLIs. Please open an
issue first for anything larger than an adapter.

## Credits

Built on [age](https://github.com/FiloSottile/age), [cobra](https://github.com/spf13/cobra),
[go-keyring](https://github.com/zalando/go-keyring), [flock](https://github.com/gofrs/flock),
[fsnotify](https://github.com/fsnotify/fsnotify), [memguard](https://github.com/awnumar/memguard)
and [testscript](https://github.com/rogpeppe/go-internal). The ideas come from
[direnv](https://direnv.net) (trust), [mise](https://mise.jdx.dev) (shims),
[aws-vault](https://github.com/99designs/aws-vault) (exec + keyring) and
[1Password Shell Plugins](https://github.com/1Password/shell-plugins), whose
catalogue was the reference for the env-var names of the API-token adapters.

## License

[MIT](LICENSE) © Adrian Issac
