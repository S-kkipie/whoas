# whoas M3 — Design Addendum

**Date:** 2026-11-06
**Status:** Approved (design), pending implementation plan
**Author:** Adrian Issac (GitHub: S-kkipie)
**Extends:** [`2026-11-05-whoas-design.md`](2026-11-05-whoas-design.md) (M1) and [`2026-11-06-whoas-m2-design.md`](2026-11-06-whoas-m2-design.md) (M2). Everything not stated here stays as in M1/M2.

## Scope

M3 adds:

- **Claude Code accounts.** Several Claude subscriptions (Pro/Max), chosen per project like any other CLI.
- **MCP servers with several accounts, for any AI agent.** A stdio proxy, `whoas mcp proxy`, injects the API key of the identity chosen for the project. Built in: PostHog and Linear.

Out of M3:
- MCP servers that only support OAuth (M4: OAuth inside the proxy).
- Claude accounts that need Remote Control or claude.ai connectors (they need a `CLAUDE_CONFIG_DIR` overlay; possible later milestone).
- The Claude desktop app.
- File-based CLIs from the M2 review (aws, npm, heroku…; separate M2.5).

This replaces the M1 plan for Claude/MCP (`json-slice` overlays of `.credentials.json`). That plan needed Spike 0a, and Anthropic's refresh-token rotation makes copied sessions fragile. M3 uses long-lived tokens and API keys instead, so it needs no overlay, no lock and no refresh.

## Findings that shaped the design (official Claude Code docs, 2026-11-06)

- `claude setup-token` prints a **one-year** OAuth token for a Pro/Max/Team/Enterprise subscription (subscription billing). It does not save it.
- `CLAUDE_CODE_OAUTH_TOKEN` ranks above the saved `/login` credentials, below cloud providers, `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_API_KEY` and `apiKeyHelper`.
  - A session using it can only make model requests: **no Remote Control, no claude.ai connectors**.
  - Locally configured MCP servers still work.
  - Running `/login` inside the session switches that session to the login.
- `headersHelper` exists only in Claude Code. For servers configured at user scope it runs in `~/.claude`, not in the project. Every agent supports **stdio** MCP servers, so the proxy is the one mechanism that works everywhere.
- **PostHog MCP** (`https://mcp.posthog.com/mcp`) accepts `Authorization: Bearer <personal API key>`, created with the "MCP Server" preset.
- **Linear MCP** (`https://mcp.linear.app/mcp`) accepts `Authorization: Bearer <API key>`.

## Claude Code accounts

### Built-in adapter `claude` (kind `env-token`)
```toml
name = "claude"
bin  = "claude"
kind = "env-token"
# These outrank CLAUDE_CODE_OAUTH_TOKEN (or switch to API billing / a cloud provider).
unset_env = ["ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX"]

[[secret]]
env   = "CLAUDE_CODE_OAUTH_TOKEN"
label = "Claude subscription token"

[login]
cmd  = ["claude", "setup-token"]
hint = "Pick the account in your browser, then paste the token claude prints."
```

### Schema change: `login.cmd` on `env-token`
M2 forbade `login.cmd` on `env-token` adapters. M3 allows it.
- **Meaning:** a command run interactively before the secret prompts. It gets whoas's stdin/stdout/stderr and the injection-free environment (`base − unset_env − secret envs`). Its job is to help the user obtain the token.
- **Rule:** `login.cmd[0] == bin`, as for `whoami`.
- **Failure:** if the command exits non-zero, `add` stops before prompting.
- **`--stdin`:** with `whoas add <p> <alias> --stdin`, `login.cmd` is **not** run. That form is for scripts.

### Behaviour
- **`whoas add claude max`:** shows the hint, runs `claude setup-token`, then prompts `Claude subscription token (CLAUDE_CODE_OAUTH_TOKEN): ` with echo off, and seals.
  - No whoami: Claude has none that doesn't spend quota, so the account label is empty.
  - Generating a token does not touch the user's saved `/login` or running sessions.
- **Run** (explicit `whoas claude@max`, a `claude` shim with a profile or `.whoas.yaml`, `whoas run`): the M2 env-token path (`RunEnv`). Concurrent sessions with different accounts work, since there is no lock.
- **No identity for `claude` in the chosen source:** the shim passes through to the user's normal login (M1 rule).
- **`whoas doctor`:** warns when `settings.json` (in `$CLAUDE_CONFIG_DIR` or `~/.claude`) sets `apiKeyHelper`. An `apiKeyHelper` outranks the token and cannot be cleared from the environment.

## MCP servers (any agent)

### New adapter kind `mcp`
```toml
name = "mcp-posthog"
kind = "mcp"

[mcp]
server  = "posthog"                              # default server name when registering
url     = "https://mcp.posthog.com/mcp"
headers = { Authorization = "Bearer {POSTHOG_API_KEY}" }

[[secret]]
env   = "POSTHOG_API_KEY"                        # template variable; never exported
label = "PostHog personal API key (MCP Server preset)"

[login]
hint = "Create one at https://app.posthog.com/settings/user-api-keys with the MCP Server preset"
```

Validation (load error otherwise):
- **Forbidden:** `bin`, `[dir]`, `unset_env`, `[whoami]`, `login.cmd`.
- **`mcp.server`:** matches the provider-name regex and is unique among `mcp` adapters. A user adapter replaces a built-in with the same `name` or the same `mcp.server` (the M2 replace-by-bin rule, applied to `server`).
- **`mcp.url`:** `https://`, or `http://` only for a loopback host (tests and local servers).
- **`mcp.headers`:** ≥ 1 entry.
  - Every header name is a valid HTTP token.
  - Every `{NAME}` in a value is a declared `[[secret]]`, and every secret is used by some header.
  - Values may not contain CR or LF.
- **`[[secret]]`:** the M2 rules (≥ 1 required secret; env-name regex; unique).
- **`mcp.auth`:** reserved for M4 (`"oauth"`). Only absent or `"header"` is accepted.

Built-ins:

| Adapter | Server | URL | Header | Secret |
|---|---|---|---|---|
| `mcp-posthog` | `posthog` | `https://mcp.posthog.com/mcp` | `Authorization: Bearer {POSTHOG_API_KEY}` | PostHog personal API key (MCP Server preset) |
| `mcp-linear` | `linear` | `https://mcp.linear.app/mcp` | `Authorization: Bearer {LINEAR_API_KEY}` | Linear API key |

PostHog EU users override `mcp-posthog` with a user adapter pointing at their region's URL. The README shows how.

### Commands
- **`whoas add mcp-posthog acme`:** the M2 secret prompt (hidden; `--stdin` supported) and seal. `ls` and `rm` behave as for `env-token`. There is no `import`: these keys never live in a CLI's config.
- **`whoas mcp proxy <server | provider | provider@alias>`:** a stdio MCP server.
  1. Resolve the identity (see Resolution).
  2. Decrypt, agent first.
  3. Render the headers.
  4. Connect to `mcp.url` with the Streamable HTTP client transport.
  5. Forward JSON-RPC messages both ways until either side closes.

  Details:
  - **Argument:** `provider@alias` is explicit. A bare word is matched first against adapter names, then against `mcp.server`. A word that matches no `mcp` adapter is a usage error.
  - **Implementation:** the official `github.com/modelcontextprotocol/go-sdk`. Stdio server transport ↔ Streamable HTTP client transport, bridged at message level. Headers are added by an `http.RoundTripper`, along with `User-Agent: whoas/<version>`. This is M3's only new Go module.
  - **Secrets:** they exist only in the proxy's memory, never in env, argv, files or logs.
  - **Errors:** written to stderr, which agents log. Exit codes:
    - 2 — no identity resolves: `whoas: mcp proxy posthog: no identity for mcp-posthog here (profile work); add one with whoas add mcp-posthog <alias>`.
    - 3 — the vault cannot be read without a terminal: `…; run whoas unlock`.
    - 1 — the remote fails before or during the session.
- **`whoas mcp setup <server> --agent claude|codex [--scope user|local|project] [--replace] [--alias <a>]`:** registers the proxy in an agent through that agent's own CLI.
  - **Config:** command = absolute path of the running whoas; args = `["mcp","proxy","<server>"]`, or `["mcp","proxy","<provider>@<alias>"]` with `--alias`.
  - **claude:** `claude mcp add-json --scope <scope> <server> '{"type":"stdio","command":…,"args":[…]}'`. Default scope: `user`.
  - **codex:** `codex mcp add <server> -- <whoas> mcp proxy <server>`. Codex has no scopes, so `--scope` is an error there.
  - **Existing entry:** if the agent already has a server with that name, setup fails and names it, unless `--replace` is given (remove, then add).
  - **Output:** prints the agent command it ran. The argv holds no secret.
- **`whoas mcp setup <server> --print`:** prints the generic stdio JSON entry, to paste into other agents (Cursor, Gemini CLI, opencode, Hermes). The README has a snippet per agent.

### `whoas providers`
`mcp` adapters show the kind `mcp` and `n/a` in INSTALLED. They are always listed in the default view, since they need no binary.

## Resolution context

The proxy runs wherever the agent spawns it. To know the project it belongs to:

- **Context variable:** every process whoas starts or execs gets `WHOAS_CONTEXT_DIR=<the directory whoas resolved from>`. That covers explicit runs, shims (including passthrough), `whoas run` and `login.cmd`. Nested shims overwrite it with their own cwd. So `claude` or `codex` started through a shim in a repo carries that repo to every MCP proxy it spawns.
- **Proxy order** (the M1 chain):
  1. explicit `provider@alias` argument
  2. `WHOAS_PROFILE`
  3. the nearest trusted `.whoas.yaml`, walking up from `WHOAS_CONTEXT_DIR`, or from the cwd when unset
  4. the default profile

  Profile keys and `.whoas.yaml` lines use the adapter name (`mcp-posthog: acme`).
- **Pinning:** the identity is fixed when the proxy starts. A reconnect starts a new proxy and resolves again.
- **`whoas which mcp-posthog`:** explains the choice, as for CLIs.

## Security

- **Claude token:** `CLAUDE_CODE_OAUTH_TOKEN` must be in Claude's environment. Commands the agent runs can read it. Documented.
- **MCP keys:** they never enter any environment. Only the proxy process holds them.
- **Agents with a shell:** they can use any identity their profile grants, by starting `whoas mcp proxy` or `whoas <cli>@x` (as in M1). Documented.
- **Untrusted repos:** an untrusted `.whoas.yaml` is ignored (M1). A repo's `.mcp.json` can only name an alias, never a secret, and agents ask before trusting a project's `.mcp.json`.
- **Agent configs:** `mcp setup` writes only the whoas path and arguments.

## Testing

Unit:
- `mcp` kind validation (every rule above).
- `env-token` with `login.cmd` (allowed; `cmd[0] == bin`).
- Header rendering.
- The RoundTripper adds headers and never logs them.
- The message bridge closes both sides when one ends.
- Resolution with and without `WHOAS_CONTEXT_DIR`.
- Proxy exit codes 2 and 3.
- The doctor `apiKeyHelper` warning.

e2e (testscript; helper binaries built in `TestMain`):
- **`fakemcp`:** a Streamable HTTP MCP server built with go-sdk. It requires a bearer and exposes a `whoami` tool that returns which key it received.
- **`mcpcall`:** an MCP client built with go-sdk. It starts `whoas mcp proxy …` over stdio, calls `whoami` and prints the result.
- Scripts check:
  - two identities in two directories and profiles get different keys;
  - an explicit `@alias` argument wins;
  - no identity → exit 2;
  - a locked vault → exit 3;
  - no secret appears in stderr;
  - `mcp setup` with fake `claude` and `codex` (copies of fakecli that record argv) runs the expected commands, with `--replace` and `--print`;
  - `whoas add fakeclaude` with a `login.cmd` and `--stdin`, and the run injects the token, removes `ANTHROPIC_API_KEY` and sets `WHOAS_CONTEXT_DIR`.

Manual, on the author's machine:
- Two Claude accounts via `setup-token`; `whoas claude@a` and `whoas claude@b` in parallel.
- A real PostHog key: `whoas mcp setup posthog --agent claude --replace`, then `claude mcp list` shows it connected; two repos with different `.whoas.yaml` see different PostHog projects.
- The same with `--agent codex`.

## Risks

| Risk | Mitigation |
|---|---|
| `claude setup-token` output or flow changes | whoas only runs it and prompts; the user pastes whatever it prints |
| Token sessions lack Remote Control/connectors | documented; the user's normal login is untouched and used whenever no identity applies |
| go-sdk transport API changes | pin a released version; the bridge is a small, isolated package with its own tests |
| An agent spawns stdio servers outside the project and without `WHOAS_CONTEXT_DIR` | falls back to the default profile; `--alias` in a project `.mcp.json` pins it |
| An MCP server changes how it accepts API keys | data-only adapter; the user overrides it with a user adapter |
