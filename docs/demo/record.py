#!/usr/bin/env python3
"""Record the README demo as an asciicast v2 file.

Usage: record.py SANDBOX OUT.cast   (demo.sh runs it; SANDBOX/bin holds whoas and gh)

Everything happens inside SANDBOX: a throwaway HOME, whoas config/data/runtime
dirs, a master key generated for this run, a fake gh that answers from its
hosts.yml (no network), and two fake GitHub logins named like the README's,
octocat and octo-corp. The commands really run, in a pty so whoas prints its
banner; only the typing is simulated. Standard library only.
"""

import json
import os
import pty
import random
import secrets
import subprocess
import sys

COLS, ROWS = 80, 24  # tall enough that nothing scrolls away

# (command, seconds to hold after its output); "  # " starts a comment shown in grey
SCENES = [
    ("whoas ls", 2.4),
    ("whoas gh@work api user --jq .login", 1.4),
    ("whoas gh@personal api user --jq .login", 2.0),
    ("whoas profile set work gh@work claude@work", 1.4),
    ("cd ~/code/client", 0.4),
    ("echo 'profile: work' > .whoas.yaml", 0.5),
    ("whoas trust", 1.6),
    ("gh api user --jq .login   # plain gh, through the shim", 2.2),
    ("whoas which gh", 4.0),
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


def gh_login(home, user):
    """What `gh auth login --insecure-storage` would leave behind (fake token)."""
    d = os.path.join(home, ".config/gh")
    os.makedirs(d, mode=0o700, exist_ok=True)
    token = "fake-demo-token"
    with open(os.path.join(d, "hosts.yml"), "w") as f:
        f.write(
            f"github.com:\n    users:\n        {user}:\n            oauth_token: {token}\n"
            f"    git_protocol: https\n    user: {user}\n    oauth_token: {token}\n"
        )


def setup(env, home):
    """The part the demo doesn't show: capture two gh logins and a claude token."""

    def whoas(*args, stdin=None):
        r = subprocess.run(["whoas", *args], env=env, cwd=home, input=stdin, capture_output=True, text=True)
        if r.returncode != 0:
            sys.exit(f"record.py: setup: whoas {' '.join(args)} failed ({r.returncode}):\n{r.stderr}")

    gh_login(home, "octo-corp")
    whoas("import", "gh", "work")
    gh_login(home, "octocat")  # gh's own session stays on octocat
    whoas("import", "gh", "personal")
    whoas("add", "claude", "work", "--stdin", stdin="sk-ant-oat01-fake-demo-token\n")
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
    shown = "~" + cwd[len(home):] if cwd.startswith(home) else cwd
    return f"\x1b[1;34m{shown}\x1b[0m \x1b[1;32m$\x1b[0m "


def record(env, home):
    rnd = random.Random(7)  # same typing rhythm on every run
    cast = Cast()
    cwd = home
    for cmd, hold in SCENES:
        cast.out(prompt(cwd, home), 0.0)
        code, _, comment = cmd.partition("  # ")
        code = code.rstrip()
        cast.t += 0.5
        for ch in code:
            cast.out(ch, rnd.uniform(0.035, 0.075))
        if comment:
            cast.out("\x1b[90m", 0.0)
            for ch in "   # " + comment:
                cast.out(ch, rnd.uniform(0.015, 0.03))
            cast.out("\x1b[0m")
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

    # nothing from the recording machine or the fake secrets may reach the GIF
    text = "".join(e[2] for e in cast.events)
    leaks = [s for s in (sb, real_home, env["WHOAS_MASTER_KEY"], "fake-demo-token", "sk-ant-") if s and s in text]
    if len(real_user) > 2 and real_user in text:
        leaks.append("$USER")
    if leaks:
        sys.exit(f"record.py: the recording would show {leaks}; refusing to write it")

    header = {"version": 2, "width": COLS, "height": ROWS, "title": "whoas", "env": {"TERM": "xterm-256color", "SHELL": "/bin/sh"}}
    with open(dest, "w") as f:
        f.write(json.dumps(header) + "\n")
        for e in cast.events:
            f.write(json.dumps(e) + "\n")
    print(f"record.py: {len(cast.events)} events, {cast.t:.1f}s", file=sys.stderr)


if __name__ == "__main__":
    main()
