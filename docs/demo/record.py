#!/usr/bin/env python3
"""Record the README demo as an asciicast v2 file.

Usage: record.py SANDBOX OUT.cast   (demo.sh runs it; SANDBOX/bin holds whoas,
vercel and gcloud)

Everything happens inside SANDBOX: a throwaway HOME, whoas config/data/runtime
dirs, a master key generated for this run, fake vercel and gcloud CLIs that
answer from the session files whoas materializes (no network), fake logins for
a made-up developer (ana) and her client (client.dev), and fake PostHog and
Linear MCP keys. The commands really run, in a pty so whoas prints its banner;
only the typing is simulated. Standard library only.
"""

import json
import os
import pty
import random
import re
import secrets
import shutil
import subprocess
import sys

COLS, ROWS = 80, 24

# (line typed at the prompt, seconds to hold after its output). A "# " line is a
# shell comment: typed in grey, it explains the command after it and runs
# nothing. A command may go on after a backslash-newline, shown on a "> "
# continuation line as an interactive shell would.
SCENES = [
    ("# your saved accounts (each session is sealed in an encrypted vault)", 0.3),
    ("whoas ls", 2.4),
    ("# run a CLI as a specific account", 0.3),
    ("whoas vercel@client whoami", 1.2),
    ("whoas gcloud@personal config get-value account", 1.6),
    ("# group the client's accounts into a profile", 0.3),
    ("whoas profile set client vercel@client gcloud@client \\\n    mcp-posthog@client mcp-linear@client", 1.2),
    ("# pin that profile to the project folder", 0.3),
    ("cd ~/code/client", 0.4),
    ("echo 'profile: client' > .whoas.yaml", 0.5),
    ("whoas trust", 1.4),
    ("# plain commands now use the project's accounts (through the shims)", 0.3),
    ("vercel whoami", 1.2),
    ("gcloud config get-value account", 1.8),
    ("# MCP servers in Claude Code, Codex or Cursor get the project's keys", 0.3),
    ("whoas which mcp-posthog", 0.6),
    ("whoas which mcp-linear", 3.2),
]

# --- age identity (bech32, as age-keygen prints it) -------------------------

BECH32 = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"


def _polymod(values):
    gen = [0x3B6A57B2, 0x26508E6D, 0x1EA119FA, 0x3D4233DD, 0x2A1462B3]
    chk = 1
    for v in values:
        top = chk >> 25
        chk = (chk & 0x1FFFFFF) << 5 ^ v
        for i in range(5):
            if (top >> i) & 1:
                chk ^= gen[i]
    return chk


def age_identity():
    hrp = "age-secret-key-"
    data, acc, bits = [], 0, 0
    for byte in secrets.token_bytes(32):
        acc, bits = (acc << 8) | byte, bits + 8
        while bits >= 5:
            bits -= 5
            data.append((acc >> bits) & 31)
    if bits:
        data.append((acc << (5 - bits)) & 31)
    expanded = [ord(c) >> 5 for c in hrp] + [0] + [ord(c) & 31 for c in hrp]
    pm = _polymod(expanded + data + [0] * 6) ^ 1
    check = [(pm >> 5 * (5 - i)) & 31 for i in range(6)]
    return (hrp + "1" + "".join(BECH32[d] for d in data + check)).upper()


# --- sandbox ----------------------------------------------------------------


def sandbox(sb):
    home = os.path.join(sb, "home")
    env = {
        "HOME": home,
        "PATH": os.pathsep.join([os.path.join(home, ".local/share/whoas/shims"), os.path.join(sb, "bin"), "/usr/bin", "/bin"]),
        "TERM": "xterm-256color",
        "LANG": "C.UTF-8",
        "XDG_CONFIG_HOME": os.path.join(home, ".config"),
        "XDG_DATA_HOME": os.path.join(home, ".local/share"),
        "XDG_CACHE_HOME": os.path.join(home, ".cache"),
        "WHOAS_CONFIG_DIR": os.path.join(home, ".config/whoas"),
        "WHOAS_DATA_DIR": os.path.join(home, ".local/share/whoas"),
        "WHOAS_RUNTIME_DIR": os.path.join(sb, "run"),
        "WHOAS_MASTER_KEY": age_identity(),
        "WHOAS_BANNER": "always",
    }
    os.makedirs(os.path.join(home, "code/client"), mode=0o700)
    return home, env


def write(path, text):
    os.makedirs(os.path.dirname(path), mode=0o700, exist_ok=True)
    with open(path, "w") as f:
        f.write(text)


def vercel_login(home, alias):
    """What `vercel login` would leave behind; fake-vercel knows these tokens."""
    d = os.path.join(home, ".local/share/com.vercel.cli")
    shutil.rmtree(d, ignore_errors=True)
    write(os.path.join(d, "auth.json"), json.dumps({"token": f"fake-demo-token-vercel-{alias}"}, indent=2) + "\n")
    write(os.path.join(d, "config.json"), json.dumps({"currentTeam": f"team_{alias}", "telemetry": {"enabled": False}}, indent=2) + "\n")


def gcloud_login(home, account, project):
    """What `gcloud auth login` would leave behind (fake credentials)."""
    d = os.path.join(home, ".config/gcloud")
    shutil.rmtree(d, ignore_errors=True)
    write(os.path.join(d, "active_config"), "default")
    write(os.path.join(d, "configurations/config_default"), f"[core]\naccount = {account}\nproject = {project}\n")
    adc = {"type": "authorized_user", "client_id": "fake-demo", "refresh_token": "fake-demo-token-gcloud"}
    write(os.path.join(d, "legacy_credentials", account, "adc.json"), json.dumps(adc, indent=2) + "\n")


def setup(env, home):
    """The part the demo doesn't show: capture two vercel and two gcloud logins,
    add a PostHog and a Linear MCP key, install the shims."""

    def whoas(*args, stdin=None):
        r = subprocess.run(["whoas", *args], env=env, cwd=home, input=stdin, capture_output=True, text=True)
        if r.returncode != 0:
            sys.exit(f"record.py: setup: whoas {' '.join(args)} failed ({r.returncode}):\n{r.stderr}")

    vercel_login(home, "client")
    whoas("import", "vercel", "client")
    vercel_login(home, "personal")  # vercel's own session stays on personal
    whoas("import", "vercel", "personal")
    gcloud_login(home, "ana@client.dev", "client-prod")
    whoas("import", "gcloud", "client")
    gcloud_login(home, "ana@gmail.com", "ana-side-project")
    whoas("import", "gcloud", "personal")
    whoas("add", "mcp-posthog", "client", "--stdin", stdin="fake-demo-token-posthog\n")
    whoas("add", "mcp-linear", "client", "--stdin", stdin="fake-demo-token-linear\n")
    whoas("shims", "install")


# --- recording --------------------------------------------------------------


def run_in_pty(cmd, cwd, env):
    pid, fd = pty.fork()
    if pid == 0:
        try:
            import fcntl
            import struct
            import termios

            fcntl.ioctl(0, termios.TIOCSWINSZ, struct.pack("HHHH", ROWS, COLS, 0, 0))
            os.chdir(cwd)
            os.execve("/bin/sh", ["/bin/sh", "-c", cmd], env)
        finally:
            os._exit(127)
    out = b""
    while True:
        try:
            chunk = os.read(fd, 4096)
        except OSError:  # EIO: the command exited and closed the pty
            break
        if not chunk:
            break
        out += chunk
    os.close(fd)
    _, status = os.waitpid(pid, 0)
    return os.waitstatus_to_exitcode(status), out.decode()


class Cast:
    def __init__(self):
        self.t = 0.0
        self.events = []

    def out(self, text, delay=0.0):
        self.t += delay
        self.events.append([round(self.t, 3), "o", text])


def prompt(cwd, home):
    """The working directory's last element, like zsh's %1~ ("~" at home)."""
    shown = "~" if cwd == home else os.path.basename(cwd)
    return f"\x1b[1;34m{shown}\x1b[0m \x1b[1;32m$\x1b[0m "


PS2 = "\x1b[1;32m>\x1b[0m "
GREY = "\x1b[38;5;246m"


def record(env, home):
    rnd = random.Random(7)  # same typing rhythm on every run
    cast = Cast()
    cwd = home
    for code, hold in SCENES:
        cast.out(prompt(cwd, home), 0.0)
        if code.startswith("# "):  # a comment: typed a little faster, runs nothing
            cast.out(GREY, 0.25)
            for ch in code:
                cast.out(ch, rnd.uniform(0.012, 0.022))
            cast.out("\x1b[0m")
            cast.out("\r\n", 0.25)
            cast.t += hold
            continue
        cast.t += 0.3
        for i, part in enumerate(code.split("\n")):
            if i:
                cast.out("\r\n" + PS2, 0.2)
            for ch in part:
                cast.out(ch, rnd.uniform(0.022, 0.042))
        cast.out("\r\n", 0.25)
        if code.startswith("cd "):
            cwd = os.path.normpath(code[3:].replace("~", home, 1))
        else:
            status, output = run_in_pty(code, cwd, env)
            if status != 0:
                sys.exit(f"record.py: `{code}` exited {status}:\n{output}")
            output = output.replace(home, "~")
            for i, line in enumerate(output.splitlines(keepends=True)):
                cast.out(line, 0.12 if i == 0 else 0.03)
        cast.t += hold
    cast.out(prompt(cwd, home), 0.0)
    return cast


def main():
    if len(sys.argv) != 3:
        sys.exit(__doc__)
    sb, dest = os.path.abspath(sys.argv[1]), sys.argv[2]
    real_home, real_user = os.path.expanduser("~"), os.environ.get("USER", "")
    home, env = sandbox(sb)
    setup(env, home)
    cast = record(env, home)

    # nothing from the recording machine or the fake secrets may reach the GIF,
    # nor anything shaped like a real Anthropic/OpenAI, PostHog or Linear key
    text = "".join(e[2] for e in cast.events)
    leaks = [s for s in (sb, real_home, env["WHOAS_MASTER_KEY"], "fake-demo-token", "sk-", "phx_", "lin_api_") if s and s in text]
    if len(real_user) > 2 and real_user in text:
        leaks.append("$USER")
    if leaks:
        sys.exit(f"record.py: the recording would show {leaks}; refusing to write it")
    # and no line may wrap: the GIF is only COLS wide
    for line in re.sub(r"\x1b\[[0-9;]*m", "", text).replace("\r", "").split("\n"):
        if len(line) > COLS:
            sys.exit(f"record.py: this line is wider than {COLS} columns:\n{line}")

    header = {"version": 2, "width": COLS, "height": ROWS, "title": "whoas", "env": {"TERM": "xterm-256color", "SHELL": "/bin/sh"}}
    with open(dest, "w") as f:
        f.write(json.dumps(header) + "\n")
        for e in cast.events:
            f.write(json.dumps(e) + "\n")
    print(f"record.py: {len(cast.events)} events, {cast.t:.1f}s", file=sys.stderr)


if __name__ == "__main__":
    main()
