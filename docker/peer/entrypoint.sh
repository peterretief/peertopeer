#!/bin/sh
set -eu

# One-shot commands do not start a second VPN.
if [ "$#" -gt 0 ]; then
	exec "$@"
fi

: "${TS_HOSTNAME:?Set a unique TS_HOSTNAME for this peer}"
: "${DSTORE_MEMBERS:?Set the allowed mesh device names in DSTORE_MEMBERS}"
config=/var/lib/dstore/node.json
mkdir -p /var/lib/dstore "${TS_STATE_DIR:-/var/lib/tailscale}"
if [ ! -f "$config" ]; then
	dstore init -config "$config" -name "$TS_HOSTNAME" \
		-share "${DSTORE_SHARE_ID:-personal}" -members "$DSTORE_MEMBERS" \
		-quota-bytes "${DSTORE_QUOTA_BYTES:-1073741824}" \
		-max-file-bytes "${DSTORE_MAX_FILE_BYTES:-67108864}"
fi

# dstore's tailscale CLI calls must reach this daemon's LocalAPI.
export TS_SOCKET=/var/run/tailscale/tailscaled.sock
containerboot &
tailscale_pid=$!
dstore_pid=
cleanup() {
	trap - EXIT INT TERM
	if [ -n "$dstore_pid" ]; then
		kill -TERM "$dstore_pid" 2>/dev/null || true
		wait "$dstore_pid" 2>/dev/null || true
	fi
	kill -TERM "$tailscale_pid" 2>/dev/null || true
	wait "$tailscale_pid" 2>/dev/null || true
}
trap cleanup EXIT
trap 'exit 0' INT TERM

i=0
until tailscale status --json 2>/dev/null | grep -q '"BackendState":[[:space:]]*"Running"'; do
	if ! kill -0 "$tailscale_pid" 2>/dev/null; then
		echo "dstore-peer: Tailscale exited during enrollment" >&2
		exit 1
	fi
	if [ "$i" -ge "${DSTORE_TAILSCALE_WAIT_SECONDS:-180}" ]; then
		echo "dstore-peer: timed out waiting for Tailscale enrollment" >&2
		exit 1
	fi
	i=$((i + 1))
	sleep 1
done

dstore node -config "$config" &
dstore_pid=$!
# BusyBox ash wait -n observes either child. Losing either service restarts
# the whole peer; both the identity and shards remain on disk.
status=0
wait -n || status=$?
if [ "$status" -eq 0 ]; then
	status=1
fi
exit "$status"
