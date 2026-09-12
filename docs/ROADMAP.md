# Device Sharing Roadmap

## Direction

Keep the existing Go agent, AES-256-GCM encryption, Reed-Solomon coding, and
portable manifests. Start with Linux nodes on the existing Headscale/Tailscale
mesh. A device joins the VPN separately from opting into storage sharing.
Internet connectivity alone does not make a device a storage peer.

The deployed encoding is 5 data shards plus 2 parity shards. Seven shard
placements are used, and five shards per chunk are sufficient for restore. Older
2+1 manifests remain readable. Placing multiple
shards on one device reduces device-failure protection.

## Milestone 1: Linux Participation

- Versioned local configuration: sharing group, member identities, device role,
  contribution quota, file-size limit, paths, and peer endpoints.
- Authenticated, versioned capabilities endpoint. Probe the actual agent before
  automatic placement; report VPN presence separately from storage readiness.
- Linux mesh-only listener, bounded uploads, enforced quota, and writer ownership.
- Explicit file/directory addition that retains sources and preserves hierarchy.
- Verified remote placement before publishing a manifest; no silent local-only
  success when the mesh cannot accept data.
- Portable restore and directory restore with collision protection.
- Reproducible user-service installation and integration tests with peer loss.

Acceptance: three test agents accept encrypted shards; a nested directory can be
added and restored with one peer offline, without touching the source. Unknown
members, corrupt uploads, quota exhaustion, and unavailable peers fail visibly.

## Milestone 2: Durability and Recovery

- Durable job journal and resumable uploads with exponential retry/backoff.
- Manifest inventory and availability checks, followed by owner-authorized repair.
- Keep sufficient independent copies before retiring a device or deleting data.
- Encrypted manifest backups and key recovery. Losing the only manifest loses
  access even when every shard survives.
- Capacity forecasting from measured use, retention settings, and explicit user
  preferences. Forecasts advise users; they do not silently change retention.

Acceptance: restart during upload, full disks, permanent peer loss, and manifest
recovery are tested. Repair survives failure without reducing recoverability.

## Milestone 3: Sharing and User Experience

- Stable mesh node identifiers and explicit enrollment/removal, replacing the
  initial hostname-based membership configuration.
- Multiple sharing groups, recipient key envelopes, read/write permissions,
  audit history, and versioned files with conflict handling.
- An accessible desktop/web interface for devices, jobs, capacity, and restores.
- Explain actual recoverability by independent device, rather than shard count.
- Consent-based usage preferences: metered networking, battery, scheduled work,
  storage budgets, retention, and notification thresholds.

An emailed v1 manifest is a bearer capability containing its file key. Removing
a member cannot revoke keys or plaintext that member already received. Future
revocation requires re-encryption and key rotation for future access.

## Milestone 4: More Devices

- Build/test native macOS and Windows agents against the same protocol.
- ARM Linux/NAS packages and storage-only headless appliances.
- Mobile clients that upload/restore without promising always-on contribution.
- An authenticated gateway for browsers and devices unable to run the VPN or Go
  agent. Terminate their authentication at the gateway; never expose the current
  mesh HTTP protocol directly to the public internet.
- Introduce additional discovery/identity adapters only with a real second
  transport. Keep the shard and manifest contracts independent of Tailscale.

## Boundaries of Milestone 1

One sharing group per agent. Membership is configured locally and must agree on
each device. Quotas apply to ciphertext bytes, not filesystem overhead. There is
no automatic repair, garbage collection, resumable transfer, multi-user UI,
public internet enrollment, or automatic deployment to other people's machines.
New files are processed in bounded chunks with an explicit size limit. Failed batches may
leave successfully uploaded encrypted shards; do not garbage-collect them by age.

## Upstream References

- [Linux VPN installation](https://tailscale.com/docs/install/linux)
- [Tailscale CLI and identity lookup](https://tailscale.com/docs/reference/tailscale-cli)
- [Headscale client registration](https://docs.headscale.org/usage/getting-started/)

This roadmap updates the original v1 non-goals in AGENT.md for the broader
device-sharing direction. The existing encryption and storage design remains
the foundation.
