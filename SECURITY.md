# Security policy

whoas holds the keys to your accounts, so security reports are very welcome.

## Reporting a vulnerability

**Please do not open a public issue.** Report it privately through GitHub:
**Security → Report a vulnerability**, or directly at
<https://github.com/S-kkipie/whoas/security/advisories/new>.

Include the whoas version (`whoas version`), your OS (and whether it is WSL),
the keystore `whoas doctor` reports, and steps to reproduce. Never include real
tokens or session files; a fake value that shows the leak is enough.

You'll get an answer in the advisory thread. Once a fix is released, the advisory
is published with credit to you, unless you prefer otherwise.

## Supported versions

Only the [latest release](https://github.com/S-kkipie/whoas/releases/latest)
gets security fixes. whoas is pre-1.0; please upgrade before reporting.

## Scope

In scope, for example:

- **Vault, keystore and agent:** decrypting a sealed session without the master
  key, the master key leaking from a keystore backend (OS keychain, WSL bridge,
  passphrase file), the `whoas agent` socket answering another user, or the
  agent leaking the key itself.
- **Secret leakage:** a token or session showing up in argv, in the environment
  of a process that should not get it, in logs, error messages, `whoas ls` /
  `doctor` output, or in files outside the vault and the live overlay.
- **Overlay writes to global auth:** a run that modifies the CLI's own global
  session (e.g. `~/.config/gh/hosts.yml`), or an identity's session ending up in
  another identity.
- **MCP proxy header scoping:** `whoas mcp proxy` sending an identity's headers
  to a host other than the adapter's `mcp.url` (including through redirects),
  or using the wrong identity's key.
- **Shim resolution and `.whoas.yaml` trust:** an untrusted or modified project
  file being honoured, a hostile `.whoas.yaml` making whoas run a different binary
  or read secrets, or a shim resolving to an identity the context does not grant.

Out of scope (documented behaviour):

- An agent or process **with shell access** using identities its profile grants,
  e.g. by running `whoas <cli>@alias` or `whoas mcp proxy` itself.
- A **compromised local user account**: malware running as you can read live
  overlays, process environments, or ask the agent to decrypt.
- A compromised CLI binary, and the CLI's own child processes inheriting the
  identity.
- `WHOAS_MASTER_KEY` / `WHOAS_PASSPHRASE` being visible to processes started in
  an environment where you set them.

## Threat model

The full list of what whoas does and does not protect against is in
[README → Security](README.md#security).
