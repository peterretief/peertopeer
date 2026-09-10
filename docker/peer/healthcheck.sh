#!/bin/sh
set -eu
tailscale status --json | grep -q '"BackendState":[[:space:]]*"Running"'
ip=$(tailscale ip -4)
[ -n "$ip" ]
# Check the authenticated storage endpoint, not just a VPN address.
wget -q -T 5 -O /dev/null --header "X-Dstore-Share: ${DSTORE_SHARE_ID:-personal}" \
	"http://$ip:8080/v1/info"
