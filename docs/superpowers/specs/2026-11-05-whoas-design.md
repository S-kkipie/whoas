# whoas — Design Spec

**Date:** 2026-11-05
**Status:** Approved (design), pending implementation plan
**Author:** Adrian Issac (GitHub: S-kkipie)
**Origin:** PRD "micli — Universal CLI Identity Manager" (renamed, reshaped in brainstorming)

## Problem

Developers hold many accounts per provider — several Vercel teams, several
PostHog orgs, several GCP accounts, several MCP-server workspaces — and most
CLIs keep exactly one global session. Switching means `logout` → browser OAuth
→ `login`, every time. The pain is **OAuth sessions**, not static tokens.

AI agents (Claude Code, Codex, Hermes) make it worse: they run CLIs unattended
and inherit whatever global session happens to be active.

## Positioning

| Prior art | Gap whoas fills |
|---|---|
| 1Password Shell Plugins (per-dir multi-account, ~60 CLIs) | Requires 1Password subscription + human biometric unlock (agents can't); static tokens only, no OAuth session capture |
| aws-vault / granted | AWS only |
| `gh` native multi-account, `GH_TOKEN` | gh only; `gh auth switch` is global |
| direnv / mise env | Plaintext tokens in files; no capture, no vault |

**whoas = open-source, local, OS-keychain-backed identity layer for CLIs that
captures OAuth sessions and works unattended for AI agents.**

## Goals

1. Capture N OAuth sessions per CLI as named identities; switch with zero re-login.
2. Run the original CLI with a chosen identity, per process, concurrently, never
   modifying the user's global CLI state.
3. Encrypt every captured session at rest; plaintext exists only while in use.
4. Give agents a project/session identity without them knowing whoas exists (shims).
5. New CLIs added via declarative adapter files, no code, no core changes.

**Non-goals (MVP):** git identity (SSH keys, user.email), GUI, cloud sync, team
sharing, token rotation, enterprise secret managers, Claude Code on macOS
(keychain-backed), native Windows shims.

## Concepts

- **Identity** — `provider@alias` (`vercel@work`, `gcloud@acme`). A sealed auth
  blob plus non-secret metadata (account label from `whoami`, created, last used,
  adapter version).
- **MCP identities** — one provider per MCP server: `mcp/posthog@acme`,
  `mcp/linear@personal`. `claude@<alias>` holds only the Claude login.
- **Profile** — named map provider → alias:
  ```yaml
  profiles:
    work:     { vercel: work, gcloud: acme, claude: max, mcp/posthog: acme, mcp/linear: acme }
    personal: { vercel: personal, gcloud: me, claude: max, mcp/posthog: me }
  default_profile: personal
  ```

## Resolution

For provider P, first match wins:

1. Explicit `P@alias`.
2. `-p <profile>` flag.
3. `WHOAS_PROFILE` env var (session / agent).
4. Nearest `.whoas.yaml` walking up from cwd — **only if trusted**.
5. User `default_profile`.
6. Nothing resolved → shim passes through to the global CLI untouched;
   explicit `whoas P ...` errors.

A requested identity that does not exist is always an error; the CLI never runs.

### Project file `.whoas.yaml`

```yaml
profile: work
vercel: client-x        # optional per-provider override
```

- Within rule 4, a per-provider override beats the file's `profile`. Rules 1–3
  (explicit, `-p`, `WHOAS_PROFILE`) beat the whole file.
- Schema whitelist: `profile` plus `<provider>: <alias>` pairs. Unknown keys are
  an error. It can never declare adapters, commands, paths or env.
- Never contains secrets.
- Ignored (with a warning) until `whoas trust` records its path + content hash
  (direnv model). Any content change requires re-trust.

### Visibility

- One stderr line `whoas: vercel@work (adrian@acme.com)` when stderr is a TTY;
  silent otherwise.
- `whoas which <provider>` prints the identity that would resolve and which rule
  matched.

## CLI

```
whoas add <provider> <alias>          # capture OAuth login into a fresh overlay → seal
whoas import <provider> <alias>       # seal the current global session (no re-login)
whoas ls [provider]                   # identities + account + state; never secrets
whoas rm <provider>@<alias>
whoas <provider>[@alias] [args...]    # everything after provider is passed verbatim
whoas run [-p profile] -- <cmd...>
whoas profile set|ls|show|default
whoas which <provider>
whoas trust | untrust
whoas shims install|uninstall|ls
whoas agent | unlock | lock
whoas doctor | providers | version
```

Subcommand names are reserved; no provider may use them.

## Vault, keys, agent

### Layout

```
~/.config/whoas/config.yaml              # profiles, default, settings (0600, no secrets)
~/.config/whoas/adapters/*.toml          # user adapters
~/.local/share/whoas/
  recipient                              # age public key (not secret)
  vault/<provider>/<alias>.age           # sealed blob (0600)
  vault/<provider>/<alias>.meta.json     # account label, dates, adapter version — no tokens
  trust.json                             # trusted .whoas.yaml path+hash
  shims/<bin>                            # symlinks to the whoas binary
$XDG_RUNTIME_DIR/whoas/                  # 0700: locks, holders, overlays, agent.sock
```

Paths resolved through `adrg/xdg` (macOS/Windows equivalents).

### Encryption

- `age` X25519. Blob = age-encrypted JSON envelope
  `{v, provider, files: {relpath: {mode, b64}}, slices: {jsonpath: value}}`.
- **Sealing needs only the public key.** `add`, `import`, seal-on-write and final
  seal never require unlock. Only decrypt needs the private key — a locked agent
  mid-run cannot lose a refreshed token.

### Master key source (`whoas init`, implicit on first `add`)

1. macOS Keychain
2. Windows Credential Manager (native build)
3. **WSL** → `whoas.exe` (Windows build, same repo) invoked via interop, which
   stores/reads the key in Windows Credential Manager
4. Linux Secret Service (D-Bus)
5. Fallback: passphrase (age scrypt)

Backends 1, 2, 4 via `zalando/go-keyring`; 3 is go-keyring/`danieljoos/wincred`
on the Windows side of the interop call.

### `whoas agent`

- Same binary. Unix socket `agent.sock` (0600) + `SO_PEERCRED` uid check;
  named pipe on Windows.
- **Key never leaves the agent**: clients send ciphertext, agent returns
  plaintext (ssh-agent model). Key held in `awnumar/memguard` locked memory.
- Auto-started when the backend is slow (WSL interop) or passphrase-based.
  Not needed for fast backends.
- TTL: keychain-backed = until logout; passphrase = 8h idle (configurable).
  `whoas lock` wipes the key.

**Key loss** = vault unreadable → re-login per identity. Annoying, not
catastrophic. No key backup in MVP.

## Adapters

Declarative TOML, embedded in the binary and loadable from
`~/.config/whoas/adapters/`. Adapters declare data only (binary name, args, env
names, file paths); they never contain executable code.

```toml
name = "vercel"
bin  = "vercel"
kind = "dir-overlay"

[dir]
global     = "{xdg_data}/com.vercel.cli"
inject     = { flag = ["--global-config", "{dir}"] }   # or env = { GH_CONFIG_DIR = "{dir}" }
auth_files = ["auth.json"]

[login]
cmd  = ["vercel", "login"]
hint = "Use a private browser window if the browser is logged into another account."

[whoami]
cmd = ["vercel", "whoami"]
```

### Kinds

| Kind | Mechanism | MVP providers |
|---|---|---|
| `dir-overlay` (env) | env var points CLI at overlay dir | gh `GH_CONFIG_DIR`, gcloud `CLOUDSDK_CONFIG`, codex `CODEX_HOME`, neonctl `NEONCTL_CONFIG_DIR`, docker `DOCKER_CONFIG` |
| `dir-overlay` (flag) | flag prepended to argv | vercel `--global-config` (Windows binary under WSL → `wslpath -w`) |
| `dir-overlay` (xdg/home) | `XDG_CONFIG_HOME` / `HOME` points at overlay root | wrangler (auth `.wrangler/config/default.toml`); railway (`HOME`, **experimental**) |
| `env-token` | secret injected as env var; captured via hidden prompt | supabase `SUPABASE_ACCESS_TOKEN`, terraform `TF_TOKEN_*`, + catalog generated from 1Password shell-plugins (MIT, attributed) |
| `json-slice` | Go built-in | `claude`, `mcp/<server>` |

### Overlay

The overlay dir mirrors the adapter's `global` dir: every entry is a symlink to
the real one **except** `auth_files`, which are materialized from the vault.
Nested auth paths create real intermediate dirs with symlinked siblings. Shared
state (history, sessions, settings, logs) stays shared; only auth differs.

Adapter-specific notes:
- **vercel under WSL**: when the resolved binary is a Windows executable
  (`/mnt/c/...`), `{xdg_data}` maps to Windows `%APPDATA%` and `{dir}` is
  passed through `wslpath -w`.
- **gh**: `add` runs `gh auth login --insecure-storage` so the token lands in
  `hosts.yml` (which whoas then seals) instead of the system keyring.
- **gcloud**: `auth_files` = `credentials.db`, `access_tokens.db`,
  `legacy_credentials/`, `configurations/`, `active_config`,
  `application_default_credentials.json`.

### Capture

`whoas add vercel work`:
1. Build overlay with no auth files.
2. Run `login.cmd` inside it (inherited TTY → browser OAuth).
3. Verify `auth_files` exist; run `whoami` → account label.
4. Seal with the public key; write `meta.json`.
5. Wipe overlay.

If the `whoami` label matches an existing identity of the same provider → warn
(likely the browser reused a logged-in account).

`whoas import <provider> <alias>` seals the auth files found in the global dir.
The global dir is read, never written.

### Claude / MCP (`json-slice`, built-in)

- `CLAUDE_CONFIG_DIR` = overlay of `~/.claude` (plus a symlink for `.claude.json`;
  exact location confirmed in Spike 0a).
- `.credentials.json` composed from slices:
  `claudeAiOauth` ← `claude@x`; `mcpOAuth["<server>|<hash>"]` ← `mcp/<server>@y`.
- Unmanaged MCP slices are read from the global file and written back to it
  on exit — the user's normal state (the one documented exception to
  "global untouched").
- Seal-on-write diffs per slice and re-seals only changed identities
  (`tidwall/gjson` / `sjson`).
- Overlays keyed by composition hash. A slice shared by two concurrent
  compositions is propagated: on change, the supervisor (under that identity's
  lock) seals it and rewrites it into every other active overlay containing it.
  **Viability depends on Spike 0a.**

## Execution lifecycle

`whoas vercel@work --prod` (shims follow the same path):

1. **Resolve** identity. Find the real binary on `PATH` excluding the shims dir.
   Loop guard: `WHOAS_DEPTH` ≤ 8.
2. **Acquire** `flock run/vercel/work.lock` (`gofrs/flock`) → read `holders`
   (PIDs) → prune dead PIDs. First holder decrypts (agent or keystore) and
   materializes `run/vercel/work/`; later holders reuse it — same-identity
   processes share one dir and the CLI handles its own concurrent refresh as it
   does with its global dir today. Append own PID, release lock (held ms only).
3. **Spawn** real binary with injected args + user args verbatim, inherited
   stdio (TTY intact), env + injected vars. Ignore SIGINT/SIGQUIT (terminal
   delivers them to the child's process group); forward SIGTERM/SIGHUP
   (sudo / aws-vault model).
4. **Seal-on-write**: `fsnotify/fsnotify` watches materialized auth files;
   debounce 200ms → seal with public key. Refreshed tokens reach the vault while
   the process runs, surviving crashes and reboots even though overlays live on
   tmpfs.
5. **Exit**: lock → remove own PID → if last holder: final seal if hash changed,
   delete overlay → unlock.
6. **Exit code** = child's. If the child died by signal, whoas re-raises that
   signal on itself.

**Crash recovery:** an overlay whose holders are all dead is re-sealed if
changed and deleted — by the next run of that identity or by `whoas doctor`.

**Runtime location:** `$XDG_RUNTIME_DIR/whoas` (tmpfs); fallback
`~/.cache/whoas/run` (0700); macOS `$TMPDIR`. Windows binaries under WSL reach
overlays via `\\wsl.localhost\...` (`wslpath -w`; Spike 0b).

**Overhead target:** hot path (lock + agent socket + age decrypt + symlinks)
10–20ms; < 100ms required.

### Shims

- `whoas shims install` creates `~/.local/share/whoas/shims/<bin>` → symlink to
  the whoas binary (busybox-style dispatch on `argv[0]`), only for adapters
  whose CLI is detected. Prints the `PATH` line for the user's rc; never edits
  dotfiles.
- Unresolved shim → `syscall.Exec` the real binary (zero overhead, perfect
  passthrough).
- Resolved shim → supervisor path above.

### `whoas run -p work -- <cmd...>`

Sets `WHOAS_PROFILE=work`, prepends the shims dir to `PATH`, then
`syscall.Exec(cmd)`. Every nested CLI call goes through its shim and starts its
own supervisor. This is the agent story:

```
whoas run -p work -- claude
```

→ Claude starts with `claude@max` + MCP slices from `work`; every `vercel` /
`gcloud` the agent runs resolves to `work`.

### Errors (all before spawn; CLI never runs)

Unknown identity · untrusted `.whoas.yaml` with explicit invocation · keystore /
agent unreachable · lock busy > 10s (`identity busy: pid X`) · real binary not
found · adapter TOML invalid.

## Security

### Threat model

**Protects against:**
- Running against the wrong account (explicit resolution, banner, `which`, trust gate).
- Secrets at rest leaking via backups, dotfile repos, cloud sync, disk theft —
  vault encrypted; plaintext only on tmpfs while in use.
- Output leakage — `ls`, `doctor`, errors never print secrets; tokens injected
  via env, never argv; injected flags carry paths only.
- Malicious repositories — `.whoas.yaml` trust-by-hash + strict whitelist schema;
  project files cannot execute code or point at files.
- Other local users — 0700/0600 permissions, `SO_PEERCRED` on the agent socket.

**Does not protect against (stated in README):**
- Malware running as the same user (can read live overlays,
  `/proc/<pid>/environ`, ask the agent to decrypt). Same boundary as aws-vault
  and 1Password CLI.
- The CLI's own process tree (build scripts spawned by `vercel` inherit the
  identity) — by design.
- A compromised CLI binary.

### Invariants

- Global CLI dirs are byte-identical before/after any whoas command (except
  unmanaged Claude MCP slices, see above).
- No secret value ever appears in whoas stdout/stderr.
- No shell interpolation; `os/exec` with argv only.

## Testing

Go idiom: unit tests are `*_test.go` next to the code; end-to-end tests live in
`tests/` (testscript). (`__tests__/` is not used — the Go tool ignores
underscore-prefixed dirs.)

- **Unit:** resolver precedence table, overlay builder (nested paths), vault
  seal/open roundtrip, lock/refcount state machine, slice compose/split,
  adapter TOML validation, `.whoas.yaml` schema.
- **E2E (`rogpeppe/go-internal/testscript`)** with fake CLIs compiled in-test
  (read token from dir/env, print identity, rotate refresh token on write,
  sleep, exit with given codes/signals):
  - PRD AC-01..AC-07
  - two identities in parallel; same identity in parallel sharing one overlay
  - `kill -9` supervisor → recovery seals refreshed token
  - seal-on-write; shim passthrough; trust gate
  - global-dir hash unchanged
  - no fixture token appears in any stdout/stderr
- **Keystore:** in-memory fake backend for tests; real backends in CI matrix
  (ubuntu with gnome-keyring in container, macos, windows).
- **Real-CLI smoke** (vercel, gh, gcloud with throwaway accounts): build tag,
  manual, not in CI.

## Libraries

| Concern | Library |
|---|---|
| CLI, completions | `spf13/cobra` (dynamic `provider@<TAB>` completion; `DisableFlagParsing` for passthrough) |
| Encryption | `filippo.io/age` |
| OS keychain | `zalando/go-keyring` (+ `danieljoos/wincred` on Windows side) |
| File locks | `gofrs/flock` |
| Paths | `adrg/xdg` |
| File watching | `fsnotify/fsnotify` |
| JSON slices | `tidwall/gjson`, `tidwall/sjson` |
| Key in memory | `awnumar/memguard` |
| YAML / TOML | `goccy/go-yaml`, `BurntSushi/toml` |
| Interactive UI | `charmbracelet/huh`, `charmbracelet/lipgloss` |
| Tests | `rogpeppe/go-internal/testscript`, `stretchr/testify` |
| Release | `goreleaser` (brew, scoop, deb/rpm; npm distribution to verify) |

Design references (concepts, not code): direnv (trust), mise (shims),
aws-vault (exec + keyring), 1Password shell-plugins (env-var catalog, MIT).

## Milestones

- **M0 — Spikes (throwaway):**
  - 0a: Claude Code under `CLAUDE_CONFIG_DIR` — where `.claude.json` lives;
    does Claude re-read `.credentials.json` on 401 / external change; are
    `claudeAiOauth` and MCP refresh tokens rotated?
  - 0b: Windows `vercel` from WSL with `--global-config \\wsl.localhost\...`.
- **M1 — Core:** vault + keystores (+ `whoas.exe` WSL bridge) + agent;
  config/profiles/resolver/trust; supervisor (lock, refcount, overlay,
  seal-on-write, recovery); shims + `run`; `doctor`. Adapters: gh, gcloud,
  codex, neonctl, docker.
- **M2 — Breadth:** vercel, wrangler, railway (experimental); `env-token` kind
  (supabase, terraform); catalog import from 1Password shell-plugins.
- **M3 — Claude + MCP:** `json-slice` adapter (`claude`, `mcp/<server>`).
- **Phase 2:** Claude on macOS (keychain), native Windows shims, git identity,
  local audit log.

## Success metrics

- Identity selection overhead < 100ms (hot path target 10–20ms), excluding login/network.
- Zero writes to global CLI dirs in e2e (hash check).
- Zero secret occurrences in output across e2e suite.
- 100% exit-code and signal preservation in e2e.
- MVP providers working: gh, gcloud, codex, neonctl, docker, vercel, wrangler, claude + MCP.
- New `dir-overlay` / `env-token` provider added with a TOML file only.

## Risks

| Risk | Mitigation |
|---|---|
| Refresh-token rotation + concurrent use | Shared overlay per identity; seal-on-write; slice propagation (Spike 0a) |
| CLI changes auth file names/locations | Adapter versioning; `doctor` detects missing `auth_files`; e2e per adapter with fakes, smoke with real CLIs |
| `HOME`/`XDG` overlay side effects | Full symlink mirror; railway flagged experimental |
| WSL interop latency | Agent caches key; `whoas.exe` instead of `powershell.exe` |
| Browser reuses logged-in account on `add` | `whoami` duplicate warning + adapter `login.hint` |
| Key loss | Documented: re-login per identity |
