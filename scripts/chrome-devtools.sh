#!/bin/sh
# Launch a disposable Chromium for the chrome-devtools MCP.
#
# The MCP server is only a CDP client: it does not start a browser and expects
# one already listening at http://127.0.0.1:9222/json/version. This script
# starts a headless instance with a throwaway profile under /tmp/plexo-piwork,
# so agent testing never touches the daily profile (~/.config/chromium). It is
# not incognito; the dedicated --user-data-dir is the isolation.
#
# Usage:
#   scripts/chrome-devtools.sh start [url]   # url defaults to http://127.0.0.1:8091
#   scripts/chrome-devtools.sh stop
#   scripts/chrome-devtools.sh status
#
# Env overrides: CHROME_BIN, CHROME_DEBUG_PORT, CHROME_DEBUG_PROFILE.
# The profile is not deleted on stop: /tmp is tmpfs and clears on reboot.
set -eu

PORT="${CHROME_DEBUG_PORT:-9222}"
PROFILE="${CHROME_DEBUG_PROFILE:-/tmp/plexo-piwork/chrome-devtools}"
PIDFILE="$PROFILE/chromium.pid"
ENDPOINT="http://127.0.0.1:$PORT/json/version"
DEFAULT_URL="http://127.0.0.1:8091"

find_chrome() {
	if [ -n "${CHROME_BIN:-}" ]; then
		printf '%s\n' "$CHROME_BIN"
		return 0
	fi
	for c in chromium chromium-browser google-chrome google-chrome-stable; do
		if command -v "$c" >/dev/null 2>&1; then
			printf '%s\n' "$c"
			return 0
		fi
	done
	return 1
}

running_pid() {
	[ -f "$PIDFILE" ] || return 1
	pid=$(cat "$PIDFILE" 2>/dev/null) || return 1
	[ -n "$pid" ] || return 1
	kill -0 "$pid" 2>/dev/null || return 1
	printf '%s\n' "$pid"
}

wait_ready() {
	i=0
	while [ "$i" -lt 50 ]; do
		if curl -fsS "$ENDPOINT" >/dev/null 2>&1; then
			return 0
		fi
		i=$((i + 1))
		sleep 0.2
	done
	return 1
}

case "${1:-}" in
start)
	url="${2:-$DEFAULT_URL}"
	bin=$(find_chrome) || {
		echo "chrome-devtools: no chromium binary found (set CHROME_BIN)" >&2
		exit 1
	}
	if pid=$(running_pid); then
		echo "chrome-devtools: already running (pid $pid) at $ENDPOINT"
		exit 0
	fi
	mkdir -p "$PROFILE"
	# setsid puts the browser in its own process group, so stop can signal the
	# whole tree (chromium forks helpers) without matching processes by pattern.
	setsid "$bin" \
		--headless \
		--remote-debugging-port="$PORT" \
		--user-data-dir="$PROFILE" \
		--no-first-run \
		--no-default-browser-check \
		--disable-background-networking \
		"$url" >/dev/null 2>&1 < /dev/null &
	pid=$!
	echo "$pid" >"$PIDFILE"
	if wait_ready; then
		echo "chrome-devtools: ready (pid $pid) at $ENDPOINT"
	else
		echo "chrome-devtools: browser did not expose CDP at $ENDPOINT" >&2
		kill -TERM "-$pid" 2>/dev/null || kill -TERM "$pid" 2>/dev/null || true
		rm -f "$PIDFILE"
		exit 1
	fi
	;;
stop)
	if pid=$(running_pid); then
		kill -TERM "-$pid" 2>/dev/null || kill -TERM "$pid" 2>/dev/null || true
		echo "chrome-devtools: stopped (pid $pid)"
	else
		echo "chrome-devtools: not running"
	fi
	rm -f "$PIDFILE"
	;;
status)
	if pid=$(running_pid); then
		echo "chrome-devtools: running (pid $pid)"
		curl -fsS "$ENDPOINT" || true
		echo
	else
		echo "chrome-devtools: not running"
	fi
	;;
*)
	echo "usage: $0 start [url] | stop | status" >&2
	exit 2
	;;
esac
