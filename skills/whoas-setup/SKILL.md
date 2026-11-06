---
name: whoas-setup
description: Use when a project or AI agent must use specific accounts through whoas — e.g. "use the acme Vercel/Claude/GitHub account in this repo", "set up whoas here", "add a PostHog/Linear MCP key for this project", or when choosing whoas identities, profiles, .whoas.yaml, shims, or `whoas mcp proxy` entries for Claude Code / Codex / Cursor.
---

# Setting up a project with whoas

## Overview
whoas stores one sealed session/token per `provider@alias` and picks the alias per project. A project setup is: identities exist → a profile maps providers to aliases → the repo's `.whoas.yaml` names the profile and is trusted → shims make plain `vercel`/`claude`/`gh` go through whoas → MCP servers point at `whoas mcp proxy`. Never put a secret in argv, a file, or your output.

## 1. Discover (read-only)
```sh
whoas doctor            # keystore, agent, shims dir + "in PATH"
whoas ls                # existing provider@alias identities
whoas profile ls        # * marks the default profile
whoas providers --all   # supported providers and kinds (dir-overlay, env-token, mcp)
whoas which <provider>  # what would be used here, and why
```
Reuse identities that already exist; only add what is missing.

## 2. Identities
| Case | Command | Who runs it |
|---|---|---|
| Browser/OAuth login (vercel, wrangler, codex, neonctl, railway, gcloud, gh, docker) | `whoas add <provider> <alias>` | **The user**, in their terminal (`! whoas add …`): it opens a browser / needs a TTY |
| Copy the session the CLI is logged into now | `whoas import <provider> <alias>` | You. Only for gh, gcloud, docker, railway — never vercel, wrangler, codex, claude (their refresh tokens rotate; importing kills the global session). It copies whichever account is logged in now: confirm it first (`gh auth status`, `gcloud config get account`) |
| Claude Code subscription account | `whoas add claude <alias>` (runs `claude setup-token`, then a hidden prompt) | The user. Token must be one line `sk-ant-oat01-…` |
| API key (env-token CLIs, `mcp-posthog`, `mcp-linear`) | heredoc into `--stdin` (below) | You, if the user gave you the key; otherwise the user runs `whoas add mcp-posthog <alias>` |

```sh
whoas add mcp-posthog acme --stdin <<'EOF'
phx_...the key...
EOF
```
Never `echo`/`printf` a key, never pass it as an argument, never print it back.

## 3. Profile + project file
```sh
whoas profile set acme claude@personal vercel@acme gh@work mcp-posthog@acme   # creates or adds bindings; mcp-* go in profiles too
cd ~/work/acme-app
printf 'profile: acme\n' > .whoas.yaml      # optional per-CLI override lines: `gcloud: staging`
whoas trust                                 # required; re-run after every edit
whoas which vercel                          # → vercel@acme via project …/.whoas.yaml
```
`.whoas.yaml` holds no secrets (commit it). It applies to every subdirectory. `whoas profile default <name>` changes behaviour in EVERY folder without a `.whoas.yaml` — ask before setting it.

## 4. Shims (once per machine)
```sh
grep -n 'whoas/shims' ~/.zshrc ~/.bashrc 2>/dev/null   # already set up? then skip the rc edit
whoas shims install
# else append as the LAST PATH line of the rc file (after nvm, bun, pnpm…):
export PATH="$HOME/.local/share/whoas/shims:$PATH"
```
`whoas doctor` run from a non-interactive shell reports "in PATH: false" even when the rc file is correct — check the rc file, not only doctor.
Verify in a new shell: `command -v claude vercel` → `~/.local/share/whoas/shims/…`. A shim with no identity for the current context passes through to the normal global login.

## 5. MCP servers
- **Pinned to one account, available everywhere (Claude):** a new, uniquely named server.
  `claude mcp add-json --scope user posthog-acme '{"type":"stdio","command":"'"$(command -v whoas)"'","args":["mcp","proxy","mcp-posthog@acme"]}'`
- **Resolved by the repo's profile:** `whoas mcp proxy posthog` (no alias) uses `.whoas.yaml`, so the profile must bind `mcp-posthog@<alias>`. Put it in the repo's `.mcp.json` under a name that doesn't clash with a user-level server, e.g. `"posthog-whoas": {"command":"whoas","args":["mcp","proxy","posthog"]}` (no absolute home path in a committed file).
- `whoas mcp setup <server> --agent claude|codex` registers under the plain server name (`posthog`). If that name already exists it is usually the user's own OAuth server: **do not pass `--replace` without asking**; use a distinct name as above. `--print` gives the generic stdio entry for Cursor / Gemini CLI / opencode.
- Codex only passes allow-listed env vars to MCP servers: add `env_vars = ["WHOAS_CONTEXT_DIR","WHOAS_PROFILE"]` under `[mcp_servers.<name>]` in `~/.codex/config.toml`.

## 6. Verify
`whoas which <each provider>` inside the repo · `vercel whoami` / `gh api user --jq .login` through the shim · `claude auth status` → `"authMethod": "oauth_token"` · `claude mcp list` → ✔ Connected.

## Common mistakes
| Mistake | Fix |
|---|---|
| `whoas import vercel/wrangler/codex/claude` | `whoas add` — import of a rotating session breaks the global login |
| `.whoas.yaml` written but not trusted | `whoas trust` (it is ignored until then) |
| Shim PATH line before nvm/bun | Move it to the end of the rc file |
| `mcp setup … --replace` over an existing `posthog` | New name (`posthog-<alias>`) or ask first |
| Running `/login` inside a `claude@…` session | Never — it overwrites the global Claude login |
| Running `whoas add <oauth-cli>` yourself in a non-interactive shell | Ask the user to run it (`! whoas add …`) |
