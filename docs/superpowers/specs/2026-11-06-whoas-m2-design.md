# whoas M2 — Design Addendum

**Date:** 2026-11-06
**Status:** Approved (design), pending implementation plan
**Author:** Adrian Issac (GitHub: S-kkipie)
**Extends:** [`2026-11-05-whoas-design.md`](2026-11-05-whoas-design.md) (M1, shipped). Everything not stated here stays as in M1.

## Scope

M2 adds the CLIs whose sessions don't fit M1's sub-directory overlays, plus CLIs authenticated by API tokens:

- `vercel`, `docker`: `dir-overlay`, as in M1.
- `wrangler`, `railway` (experimental): **root overlays** (`XDG_CONFIG_HOME` / `HOME`).
- New **`env-token`** adapter kind: `supabase` (hand-written) plus a reviewed set of token-based CLIs.
- `whoas providers` UX for a long adapter list.

Out of M2: Claude Code + MCP (M3), native Windows, git identities, and the M1 deferred items listed in the M1 ledger.

## Findings that shaped the design (this machine, 2026-11-06)

- **Spike 0b resolved by inspection.** Under WSL, `vercel` is the npm shim `/mnt/c/.../npm/vercel`. It is a POSIX script that `exec`s `node` when there is no `node.exe` beside it, so the Linux node runs the Windows-installed JS. vercel therefore behaves as a Linux CLI: its config lives in `$XDG_DATA_HOME/com.vercel.cli`, and `--global-config` takes a Linux path. No `wslpath` translation is needed for npm-installed CLIs.
- `wrangler` keeps its OAuth session in `$XDG_CONFIG_HOME/.wrangler/config/default.toml` (it resolves the dir via XDG). The access token expires hourly and is refreshed in place. If `~/.wrangler` is a directory, wrangler uses it instead of the XDG path (see `conflicts`).
- `railway` keeps token + project links in `~/.railway/config.json` (with `config.lock`), resolved from `$HOME`.
- `docker` keeps inline `auths` in `~/.docker/config.json`. `credHelpers`/`credsStore` entries delegate to external stores.
- `WSLENV` does not forward Linux env vars to Windows executables (irrelevant now that vercel runs on Linux node).

## Adapter schema changes

### Root overlays
`dir.global` may now also be **exactly** `{home}`, `{xdg_config}` or `{xdg_data}`. In that case `dir.inject.env` must set the matching variable to `{dir}`:

| `global` | required injection |
|---|---|
| `{home}` | `HOME = "{dir}"` |
| `{xdg_config}` | `XDG_CONFIG_HOME = "{dir}"` |
| `{xdg_data}` | `XDG_DATA_HOME = "{dir}"` |

Any other combination is a load error. The overlay mechanics are unchanged: the whole root is mirrored as symlinks except `auth_files` (nested paths supported since M1), and new entries the CLI creates are promoted to the real root on exit.

A tool that saves an existing file by writing a new one and renaming it over the mirrored symlink (`git config --global`, some shells' history) replaces the symlink inside the overlay. Existing global entries still win at promotion, so that change is not copied back; at teardown whoas prints one line per such entry (non-auth, not a parent of an auth path, present in the global dir, no longer a symlink in the overlay), for every dir-overlay adapter:

```
whoas: warning: <provider>@<alias>: <name> was replaced during the run; the change was not copied to <global path>
```

Copying such changes back is a follow-up.

### `helper_keys`
Optional `dir.helper_keys = ["credsStore", "credHelpers"]`: top-level JSON keys of an auth file that delegate credentials to stores outside whoas. Each key must be a plain top-level key name. They are used for the warning described under Docker.

### `conflicts`
Optional `dir.conflicts = ["{home}/.wrangler"]`: paths, with the same placeholders as `global` (each must start with `{home}/`, `{xdg_config}/` or `{xdg_data}/`), that make the CLI ignore the injected dir. If any expanded path exists, `add`, `import` and every run (explicit, shim, `whoas run`) of that adapter fail before touching anything, exit 125:

```
whoas: <provider>: <path> exists and <bin> uses it instead of the isolated directory; move it (see the adapter's hint) to use identities
```

Not valid for `env-token` adapters.

### `experimental`
Optional boolean. It is shown in `whoas providers` and in the banner (`whoas: railway@x (...) [experimental]`).

### Kind `env-token`
```toml
name      = "stripe"
bin       = "stripe"
kind      = "env-token"
unset_env = []

[[secret]]
env      = "STRIPE_API_KEY"
label    = "Stripe Secret Key"
optional = false
plain    = false        # true: not secret (a host, an org) → prompt with echo

[login]
hint = "Create one at https://dashboard.stripe.com/apikeys"

# [whoami] is optional, as for dir-overlay adapters: a command that prints the
# account (never one that dumps config). A label that contains a non-plain
# secret is dropped.
```
Env-var names may contain lowercase letters (`TF_TOKEN_app_terraform_io`), here and in `unset_env`/`inject.env`.

Validation:
- `[dir]` and `login.cmd` are forbidden.
- There must be ≥ 1 `[[secret]]`, and ≥ 1 of them must not be `optional`.
- No `secret.env` is `HOME`, `XDG_CONFIG_HOME` or `XDG_DATA_HOME` (only root overlays inject those).
- Each `secret.env` matches the env-name regex and is unique.
- No `secret.env` appears in `unset_env`.
- `whoami.cmd[0] == bin` (as in M1).

## `env-token` behaviour

- **`whoas add <p> <alias>`**:
  - Shows `login.hint` if present.
  - Prompts once per secret on `/dev/tty`: `<label> (<ENV>): `. Echo is off unless the secret is `plain`. An empty answer is an error unless `optional`.
  - `--stdin` reads one line per secret, in declaration order, for scripts and CI.
  - Runs `whoami` (if defined) with the secrets injected, for the account label and the duplicate-account warning.
  - Seals an envelope whose files are `env/<ENV>` → value.
- **`whoas import <p> <alias>`**: reads every required secret from the current process environment and fails naming the missing ones. Optional secrets are taken if present. It then seals as above.
- **Run** (explicit, shim, `run`):
  - Same resolution, trust, depth and exit codes as M1.
  - Decrypt (agent first) → child env = `base − unset_env − secret envs + ENV=value…` → spawn.
  - A required secret missing from the identity (e.g. sealed by an older dir-overlay adapter of the same name) fails before spawning, exit 125: `whoas: <ref>: identity has no <ENV>; re-add it with whoas add <provider> <alias>`.
  - **No overlay, lock or seal-on-write** (tokens are static).
  - Signals and exit status go through the same supervisor helper.
- **`whoas ls`**: ACTIVE shows `-` (no holders). **`rm`**: no lock needed. **`doctor`** skips env-token adapters when recovering overlays.
- **Secrets** live only in the vault and the child's env, never in argv, stdout/stderr or logs.

## Built-in adapters added

| Adapter | Kind | global / inject | auth / secrets | unset_env | notes |
|---|---|---|---|---|---|
| `vercel` | dir-overlay | `{xdg_data}/com.vercel.cli`, flag `--global-config {dir}` | `auth.json`, `config.json` | `VERCEL_TOKEN` | `config.json` holds `currentTeam` → per identity |
| `wrangler` | dir-overlay (root) | `{xdg_config}`, `XDG_CONFIG_HOME={dir}` | `.wrangler/config` (whole dir: wrangler has its own auth profiles) | `CLOUDFLARE_API_TOKEN`, `CLOUDFLARE_API_KEY` | `conflicts = ["{home}/.wrangler"]`: wrangler prefers a legacy `~/.wrangler` dir over XDG; the hint says to move it to `~/.config/.wrangler` |
| `railway` | dir-overlay (root), experimental | `{home}`, `HOME={dir}` | `.railway/config.json` | `RAILWAY_TOKEN`, `RAILWAY_API_TOKEN` | children of `railway run` also see the overlay HOME |
| `docker` | dir-overlay | `{home}/.docker`, `DOCKER_CONFIG={dir}` | `config.json` | — | warns about `credHelpers`/`credsStore` |
| `supabase` | env-token | — | `SUPABASE_ACCESS_TOKEN` | — | |
| token CLIs | env-token | — | per CLI | per CLI | reviewed set, see below |

Login and `whoami` are pinned from the installed CLIs, read-only (`--help` and source):

| CLI | login | whoami | account field |
|---|---|---|---|
| vercel | `vercel login` | `vercel whoami --format json` | `email` |
| wrangler | `wrangler login` | `wrangler whoami --json` | `email` |
| railway | `railway login` | `railway whoami --json` | `email` (to confirm in the real smoke test) |

### Docker credential helpers
After `add`/`import`, if a sealed auth file contains one of the adapter's `helper_keys`, whoas prints:

```
warning: docker@<a>: config.json delegates credentials to helpers (credHelpers: <registries>; credsStore: <store>); those stay in the helper's own store and are not isolated by whoas
```

This is not repeated in `doctor`, which would have to decrypt every identity.

Tip, documented: any credential the CLI writes **during a run** is captured, so `whoas docker@work login ghcr.io` adds a registry to that identity.

### Token-based CLI set
- The env-var data for token-based CLIs is collected once, out of band, from the public [1Password Shell Plugins](https://github.com/1Password/shell-plugins) catalogue (MIT). The collection tool is **not** committed.
- Only plugins whose default provisioner is a plain env-var mapping qualify. Bins already covered by a hand-written adapter, and collisions, are excluded (e.g. Laravel Forge's `forge`, which is also Foundry's binary). The resulting list is reviewed by hand: 64 env-token built-ins, 72 built-ins in all.
- The adapters are committed as ordinary whoas built-ins, in the same style as the hand-written ones. The README credits 1Password Shell Plugins as the reference for the env-var names.
- The adapters contain only facts (binary names, env-var names, URLs), no third-party code.

## `whoas providers`
- Default: installed CLIs only, then a line `+N more (whoas providers --all)`.
- `--all`: every adapter.
- Columns: `PROVIDER BIN KIND SOURCE INSTALLED`, with `SOURCE` = `builtin` | `user`. Experimental adapters show `(experimental)` after the kind.
- Installed-ness is resolved with one PATH index: each PATH directory is listed once. Stat-ing every directory per adapter took 47 s on WSL, whose PATH holds ~45 `/mnt/c` directories. `shims install` uses the same index.

## Code structure
- `internal/adapter`:
  - `Adapter` gains `Experimental bool` and `Secrets []Secret` (`Secret{Env, Label string; Optional bool}`).
  - `Kind` gains `KindEnvToken`.
  - Validation follows the rules above.
  - The registry gains a `Source(name) string` method.
- `internal/supervisor`:
  - The child-process launch (signals, wait, result) is extracted into `spawnCmd(bin string, argv, env []string, stdio)`. `Plan.spawn` uses it.
  - New `RunEnv(ctx, EnvPlan) (Result, error)` for env-token runs.
- `internal/app`:
  - `runProvider`, `add` and `import` branch on `Kind`.
  - Hidden prompt and `--stdin` helpers.
  - Docker helper warning.
  - `providers --all`.
  - The banner appends ` [experimental]` for experimental adapters.

## Testing
- e2e (testscript), using extra copies of the fake CLI under other names (bins must be unique):
  - **`fakehome`**: a root `{home}` overlay. Covers add, import and run, global auth untouched, promotion of new files in `~`, concurrent runs.
  - **`faketok`**: env-token. Covers `add --stdin`, `import` from the env, injection plus `unset_env`, `ls`, profiles, shim passthrough, no secret in whoas output, blob not plaintext.
  - **docker-helper warning**: a fixture `config.json` with `credHelpers`.
- Unit tests:
  - validation (root ↔ variable, env-token schema);
  - root-overlay mirror;
  - `spawnCmd` / `RunEnv`;
  - `providers` listing;
  - **every built-in loads, validates and has a unique bin**.
- Manual, read-only smoke against the real CLIs on the author's machine: `whoami` format before writing adapters; `import` + `whoas <cli>@x whoami` after.

## Risks
| Risk | Mitigation |
|---|---|
| A tool under `railway` ignores `$HOME` | marked experimental; documented |
| Token adapter data drifts from the real CLI | a wrong or outranked variable means the CLI falls back to its stored login or an ambient variable, i.e. runs as the wrong account without an error. Mitigated by `unset_env` for known competing variables (`HUGGING_FACE_HUB_TOKEN` for `hf`, `FLY_API_TOKEN` for `fly`/`flyctl`), a README caveat to check each CLI once with a whoami-style command, and re-collecting the data when needed |
| vercel `config.json` per identity stops sharing telemetry/preferences | accepted: required to isolate `currentTeam` |
| Root overlays mirror many entries | top-level only (typically < 200 symlinks, ~1 ms); measured in tests |
