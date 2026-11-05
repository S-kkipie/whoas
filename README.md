# whoas

Run any CLI as a chosen identity. Capture several OAuth logins per CLI once,
then switch per command, per project, or per AI agent — without logging out,
without touching the CLI's global session.

```
whoas add gh work            # log in once through gh's own flow
whoas add gh personal
whoas gh@work repo list      # runs the real gh as "work"
whoas gh@personal repo list  # at the same time, as "personal"
```

- **Open source and local.** Sessions are sealed with [age](https://age-encryption.org)
  in `~/.local/share/whoas/vault`; the master key lives in your OS keychain.
- **OAuth sessions, not just tokens.** whoas snapshots what the CLI's own login
  writes, so browser logins work.
- **Built for agents.** With shims, `gh`, `gcloud`, … pick the identity from the
  project or session, so an agent that runs `gh pr create` uses the right
  account without knowing whoas exists.

> Status: early. Linux, WSL and macOS. Native Windows only ships the
> `whoas.exe` keystore helper used by WSL.

## Install

```
go install github.com/S-kkipie/whoas/cmd/whoas@latest
```

**WSL:** also put the Windows build next to the Linux one so the master key
lives in Windows Credential Manager:

```
GOOS=windows go build -o "$(dirname "$(command -v whoas)")/whoas.exe" github.com/S-kkipie/whoas/cmd/whoas
```

## Use

```
whoas providers                     # supported CLIs and whether they are installed
whoas add <provider> <alias>        # log in and save the session
whoas import <provider> <alias>     # save the CLI's current global session
whoas ls                            # identities (never prints secrets)
whoas <provider>@<alias> [args...]  # run as that identity; args pass through verbatim
whoas which <provider>              # what would resolve, and why
whoas doctor                        # check setup; clean overlays/captures left by crashed runs
```

### Profiles and projects

```
whoas profile set work gh@work gcloud@acme
whoas profile default personal
echo 'profile: work' > ~/code/client/.whoas.yaml
cd ~/code/client && whoas trust     # review first: project files need approval
whoas gh repo list                  # → gh@work
```

Resolution — the first source present decides, with no fallthrough:
`provider@alias` → `-p profile` → `WHOAS_PROFILE` → trusted `.whoas.yaml`
(a `provider: alias` line beats its `profile`) → default profile.

### Shims and agents

```
whoas shims install                 # prints the PATH line to add
whoas run -p work -- claude         # every CLI the agent runs resolves to "work"
```

A shim with no identity resolved is a zero-overhead `exec` of the real CLI.

`shims install` and `uninstall` only touch whoas's own links; anything else in
the shims dir is left alone.

### Key agent

On WSL and with the passphrase fallback, reading the master key is slow, so
whoas starts `whoas agent` to cache it (the key never leaves the agent; clients
send ciphertext). `whoas unlock` / `whoas lock` control it.

### Exit codes and signals

The CLI's exit status is passed through. If the CLI dies from SIGHUP, SIGINT
or SIGTERM, whoas re-raises that signal; any other signal exits `128+n`.
whoas's own failures exit `125`, a CLI that cannot be executed `126`, and a CLI
that is not on PATH `127`, on every run path (explicit, shim and `run`).

SIGTERM and SIGHUP are always forwarded to the CLI. SIGINT and SIGQUIT are
forwarded only when whoas is not in the terminal's foreground process group
(agents, CI); in a terminal, Ctrl-C already reaches the CLI. Signals ignored by
whoas's parent (e.g. under `nohup`) stay ignored.

## How it works

For `whoas gh@work`, whoas builds an overlay of `~/.config/gh` in
`$XDG_RUNTIME_DIR/whoas` (`$TMPDIR/whoas` on macOS): every entry is a symlink to the real one except the
auth files (`hosts.yml`), which are decrypted from the vault. gh is pointed at
the overlay (`GH_CONFIG_DIR`). Processes using the same identity share the
overlay; when gh refreshes a token it is re-sealed within ~200ms; when the last
one exits the overlay is removed. Your global `~/.config/gh/hosts.yml` is never
written.

## Adapters

Built in: `gh`, `gcloud`, `codex`, `neonctl`. Add your own in
`~/.config/whoas/adapters/<name>.toml`. Adapters are data, not scripts, but an adapter decides which binary runs, so only add adapters you trust (project files can never declare one):

```toml
name = "mycli"
bin  = "mycli"
kind = "dir-overlay"
unset_env = ["MYCLI_TOKEN"]                  # env vars that would override the identity

[dir]
global     = "{xdg_config}/mycli"            # {home} {xdg_config} {xdg_data}
inject     = { env = { MYCLI_CONFIG_DIR = "{dir}" } }   # or flag = ["--config-dir", "{dir}"]
auth_files = ["credentials.json"]

[login]
cmd = ["mycli", "login"]

[whoami]
cmd  = ["mycli", "whoami", "--json"]
json = "email"                               # optional gjson path
```

Validation: every `inject.env` value must contain `{dir}`, `global` must start
with `{home}/`, `{xdg_config}/` or `{xdg_data}/`, and no two adapters may share
a `bin`.

## Threat model

**Protects against**

- Running against the wrong account (explicit resolution, banner, `which`, trust gate).
- Sessions leaking at rest through backups, dotfile repos, cloud sync or disk
  theft: the vault is encrypted; plaintext exists only in the runtime dir (tmpfs on Linux) while in use.
- whoas leaking secrets in output: `ls`, `doctor` and errors never print them;
  injection uses env vars and paths, never tokens in argv.
- Malicious repositories: `.whoas.yaml` needs `whoas trust` (by content hash)
  and only accepts `profile` and `provider: alias` strings (max 64 KiB, no YAML anchors or aliases).
- Other local users: 0700/0600 permissions, plus a peer-uid check on the agent socket on Linux and macOS.

**Does not protect against**

- Malware running as your user (it can read live overlays,
  `/proc/<pid>/environ`, or ask the agent to decrypt). Same boundary as
  aws-vault and 1Password CLI.
- The CLI's own child processes (they inherit the identity by design).
- A compromised CLI binary.

## Credits

Ideas from [direnv](https://direnv.net) (trust), [mise](https://mise.jdx.dev)
(shims), [aws-vault](https://github.com/99designs/aws-vault) (exec + keyring)
and [1Password Shell Plugins](https://github.com/1Password/shell-plugins).

## License

MIT
