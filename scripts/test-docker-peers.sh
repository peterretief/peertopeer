#!/usr/bin/env bash
# Integration check against two already-enrolled local Docker peers.
# The temporary host node provides shard three and is stopped before restore.
set -euo pipefail
repo=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
cd "$repo"
compose=(docker compose --env-file .env.peers -f docker-compose.peers.yml)
for tool in docker tailscale jq curl go cmp; do
	command -v "$tool" >/dev/null
done
mkdir -p .docker
work=$(mktemp -d "$repo/.docker/smoke.XXXXXX")
node_pid=
cleanup() {
	if [[ -n "$node_pid" ]]; then
		kill -TERM "$node_pid" 2>/dev/null || true
		wait "$node_pid" 2>/dev/null || true
	fi
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

a_ip=$("${compose[@]}" exec -T peer-a tailscale ip -4)
b_ip=$("${compose[@]}" exec -T peer-b tailscale ip -4)
a_config=$("${compose[@]}" exec -T peer-a cat /var/lib/dstore/node.json)
b_config=$("${compose[@]}" exec -T peer-b cat /var/lib/dstore/node.json)
a_name=$(jq -er .name <<<"$a_config")
b_name=$(jq -er .name <<<"$b_config")
share=$(jq -er .share_id <<<"$a_config")
[[ "$share" == "$(jq -er .share_id <<<"$b_config")" ]]
host_ip=$(tailscale ip -4)
host_name=$(tailscale whois --json "$host_ip" | jq -er '.Node.ComputedName')
port=${DSTORE_TEST_PORT:-18082}
peers="$a_name=$a_ip:8080,$b_name=$b_ip:8080,$host_name=$host_ip:$port"
members="$a_name,$b_name,$host_name"
go build -o "$work/dstore" ./cmd/dstore
"$work/dstore" init -config "$work/node.json" -name "$host_name" \
	-share "$share" -members "$members" -port "$port" -peers "$peers" \
	-quota-bytes 16777216
"$work/dstore" node -config "$work/node.json" >"$work/node.log" 2>&1 &
node_pid=$!
ready=false
for ((attempt=0; attempt<30; attempt++)); do
	if ! kill -0 "$node_pid" 2>/dev/null; then
		cat "$work/node.log" >&2
		exit 1
	fi
	if curl --noproxy '*' -fsS --max-time 2 -H "X-Dstore-Share: $share" \
		"http://$host_ip:$port/v1/info" >/dev/null 2>&1; then
		ready=true
		break
	fi
	sleep 1
done
[[ "$ready" == true ]]
"$work/dstore" peers -config "$work/node.json"
mkdir -p "$work/source/nested"
printf 'Docker shard round-trip test\n' >"$work/source/nested/message.txt"
dd if=/dev/urandom of="$work/source/sample.bin" bs=1024 count=256 status=none
"$work/dstore" add -config "$work/node.json" "$work/source"
for stub in "$work/library/source/sample.bin.dstore" "$work/library/source/nested/message.txt.dstore"; do
	[[ "$(jq '[.shards[].peer] | unique | length' "$stub")" == 3 ]]
done
kill -TERM "$node_pid"
wait "$node_pid"
node_pid=
# No -shards fallback: only the two live Docker destinations can supply data.
"$work/dstore" restore-tree -source "$work/library/source" -output "$work/restored"
cmp "$work/source/sample.bin" "$work/restored/sample.bin"
cmp "$work/source/nested/message.txt" "$work/restored/nested/message.txt"
printf 'PASS: restored both files with the host shard server stopped; sources retained.\n'
printf 'Test artifacts and manifests: %s\n' "$work"
