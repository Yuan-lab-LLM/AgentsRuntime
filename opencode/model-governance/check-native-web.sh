#!/bin/sh
# A build can have a working API while still missing the embedded web bundle.
set -eu

workspace=$(mktemp -d)
pid=
cleanup() {
    if [ -n "$pid" ]; then
        kill "$pid" 2>/dev/null || true
        wait "$pid" 2>/dev/null || true
    fi
    rm -rf -- "$workspace"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM
cd "$workspace"
export XDG_CONFIG_HOME="$workspace/config"
export XDG_DATA_HOME="$workspace/data"
export XDG_CACHE_HOME="$workspace/cache"
export XDG_STATE_HOME="$workspace/state"
export OPENCODE_SERVER_USERNAME=opencode
export OPENCODE_SERVER_PASSWORD=local-build-check
export OPENCODE_DISABLE_AUTOUPDATE=true
origin=http://127.0.0.1:18126
opencode serve --hostname 127.0.0.1 --port 18126 >server.log 2>&1 &
pid=$!

fetch() {
    curl --fail --silent --show-error --max-time 4 --noproxy '*' \
        --user "$OPENCODE_SERVER_USERNAME:$OPENCODE_SERVER_PASSWORD" "$origin$1"
}
ready=false
for attempt in $(seq 1 40); do
    if fetch /api/info >info.json 2>/dev/null; then
        ready=true
        break
    fi
    kill -0 "$pid" 2>/dev/null || break
    sleep 0.25
done
if [ "$ready" != true ]; then
    cat server.log >&2
    exit 1
fi

fetch / >index.html
asset=$(node -e '
    const html = require("node:fs").readFileSync("index.html", "utf8");
    const match = html.match(/<script[^>]+src=["\x27](\/[^/"\x27][^"\x27]*\.js)["\x27]/i);
    if (!match) process.exit(1);
    console.log(match[1]);
')
fetch "$asset" >app.js
test -s app.js
if cmp -s index.html app.js; then
    echo "OpenCode served the HTML fallback for a JavaScript asset" >&2
    exit 1
fi
fetch /server/http%3A%2F%2F127.0.0.1%3A18126/session/build-check >session.html
cmp index.html session.html
