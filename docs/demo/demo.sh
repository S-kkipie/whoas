#!/bin/sh
# Regenerate docs/demo.gif (the README demo) from the current checkout:
#
#   docs/demo/demo.sh
#
# Needs go, python3 and agg (https://github.com/asciinema/agg/releases; put it on
# PATH or set AGG=/path/to/agg). Everything runs in a throwaway sandbox: temp
# HOME, whoas dirs, a master key made for this run, fake vercel and gcloud CLIs
# with fake logins (ana, client.dev) and fake PostHog/Linear MCP keys. The
# recording never reads or writes your real setup and never touches the network.
set -eu

root=$(cd "$(dirname "$0")/../.." && pwd)
agg=${AGG:-agg}
command -v "$agg" >/dev/null 2>&1 || { echo "demo.sh: agg not found (set AGG)" >&2; exit 1; }

sb=$(mktemp -d "${TMPDIR:-/tmp}/whoas-demo.XXXXXX")
trap 'rm -rf "$sb"' EXIT

mkdir "$sb/bin"
(cd "$root" && go build -o "$sb/bin/whoas" ./cmd/whoas)
for cli in vercel gcloud; do
	cp "$root/docs/demo/fake-$cli" "$sb/bin/$cli"
	chmod 0755 "$sb/bin/$cli"
done

python3 "$root/docs/demo/record.py" "$sb" "$sb/demo.cast"

"$agg" --quiet --theme github-dark --font-size 16 --last-frame-duration 1 \
	"$sb/demo.cast" "$root/docs/demo.gif"
ls -l "$root/docs/demo.gif"
