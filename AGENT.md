# AGENT.md — Distributed Store (dstore/chunkd)

## Purpose

A peer-to-peer file storage system for members of an existing Headscale/Tailscale
mesh. Each user contributes local storage space to hold shards of other users'
files, and pushes shards of their own files out to peers. There is no central
server and no cloud storage dependency — the mesh IS the storage.

A user drops a file into a local "origin" folder. The system encrypts it,
splits it into erasure-coded shards, and distributes those shards to other
peers currently online on the mesh. The original file is replaced by a small
stub file. Opening the stub reconstructs the original by pulling shards back
from whichever peers are reachable.

## Design Goals (in priority order)

1. **Simplicity** — minimum moving parts. No database server, no auth server,
   no cloud accounts, no central metadata service.
2. **Security** — shards must be meaningless to anyone who doesn't hold the
   file's key. Peers should not be able to read, tamper with, or grief each
   other's data.
3. **Resilience** — tolerate up to 2 of 7 shard-holding peers being offline
   or unreachable at reconstruction time.

Explicitly NOT goals for this version:
- Sharing files with people outside the mesh
- A public/central manifest or metadata service
- Peer selection/trust preferences (random peer selection is fine for v1)
- Node health tracking, resume support, or performance tuning beyond
  "works reliably for personal file sync"

## Roles (every mesh peer runs the same agent, symmetric)

Each peer's agent does two jobs simultaneously:

1. **Storage contributor** — runs `chunkd`, an HTTP server that stores and
   serves shards written to it by other peers, in a local folder dedicated
   to this purpose.
2. **Origin watcher** — watches a local "origin" folder. When a file is
   added, it triggers the shard-and-distribute flow (`dstore`'s existing
   directory-watch logic).

There are no special/dedicated nodes. Any peer can be an origin owner and a
storage contributor at the same time, for different files.

## Existing Components to Reuse (do not rewrite)

- `internal/erasure` — erasure coding (configurable layout; the deployed layout is
  5 data + 2 parity shards, 7 total, with older 2+1 manifests supported).
  Fixed bug: `erasure.Decode` no longer pre-fills nil shards (handled
  natively by `Reconstruct`).
- `internal/metadata` — existing DB operations, reusable for local-only
  bookkeeping if needed (NOT a central service — each peer's metadata is
  local to itself).
- `chunkd` — storage daemon, HTTP shard read/write. Deploy universally
  (every peer runs it), not just on 1–2 dedicated nodes as before.
- `dstore` — controller/directory-watcher. Origin-folder watching logic
  carries over largely unchanged.

## New Components Required

### 1. Encryption (mandatory, non-negotiable)

- AES-256-GCM.
- Fresh random key generated per file and fresh random nonce per encrypted chunk at shard time
  (`crypto/rand`), never reused.
- Encrypt BEFORE erasure coding, so every shard is ciphertext.
- The key is never sent to a `chunkd` peer. It lives inside the manifest stub; emailing the stub intentionally gives the recipient the restore capability. Version-2 manifests keep nonce and shard references per chunk.

### 2. Manifest

A small struct (a few hundred bytes) describing everything needed to
reconstruct a file. Fields:

```go
type Manifest struct {
    Version        int
    DataShards     int
    ParityShards   int
    FileID         string
    FileName       string
    PlaintextSize  int
    ChunkSize      int
    Key            []byte
    Nonce          []byte    // version-1 whole-file manifests
    Shards         []ShardRef // version-1 whole-file manifests
    Chunks         []Chunk    // version-2 chunked manifests
}

type ShardRef struct {
    Hash string // SHA-256 of the shard's ciphertext — this IS the shard's ID/filename on the peer
    Peer string // Tailscale IP or MagicDNS name currently holding it
    URL  string // optional HTTP URL for restoring from an emailed manifest
}
```

Shards are content-addressed: a shard's filename on the storing peer
*is* its SHA-256 hash, not an arbitrary index like `shard-0`. This
means the manifest doesn't need a separate hash field for
verification — asking a peer for hash `X` and getting back bytes that
don't hash to `X` is itself the tamper/corruption signal, with no
extra bookkeeping. It also gives free deduplication if two shards
ever happen to be identical.

The manifest is generated at shard time and is embedded in the stub file below. It can be emailed as the restore token, but it contains the AES key, so anyone with the stub and access to at least 5 shard URLs for every version-2 chunk can reconstruct the file.

### 3. Stub file (this IS the "link")

When a file is sharded, the original in the origin folder is replaced by
a stub file (e.g. `myphoto.jpg.dstore`) containing the serialized
manifest (JSON or gob, doesn't matter — it's local only).

Opening/double-clicking the stub triggers reconstruction:
1. Read manifest locally — no network call needed to know where to look.
2. Contact the listed peers over the tailnet by IP/MagicDNS name,
   requesting each shard by its content hash or by the URL embedded in
   the manifest.
3. Pull whichever shards are reachable (need 5 of 7 for each version-2 chunk).
4. Verify each returned shard's bytes hash to the requested hash before
   use — mismatch = reject and treat as unreachable.
5. `erasure.Decode` to reconstruct each ciphertext chunk.
6. AES-GCM decrypt each chunk with the manifest key and its nonce.
7. Write the reconstructed file back to disk in place of the stub (or
   open it directly).

### 4. Peer identity & access control on `chunkd`

- Use Tailscale's local API (`tailscale.WhoIs`) on every incoming
  `chunkd` request to identify the requesting peer by their enrolled
  mesh identity. No separate login system, no tokens, no passwords.
- **Reads**: open to any mesh peer, requested by content hash. Shards
  are ciphertext — no harm in any peer fetching them.
- **Writes**: since shard IDs are content-addressed (the hash of the
  bytes), a write to an existing hash is a no-op (the content is
  already correct by definition) rather than something that needs
  ownership-checking.
- **Deletes**: only permitted from the same peer identity that
  originally wrote that shard. Prevents one peer from griefing
  another's data by deleting shards out from under them. This is a
  `WhoIs` identity comparison, not a new auth system — expect ~10
  lines of code.

### 5. Peer discovery / shard placement

- Query Headscale/Tailscale for currently-online mesh peers.
- Select 7 peers from those online to receive the 7 shards of
  a new file. Random selection is intentional for v1 — simplest option,
  revisit trust/preference logic later without changing the manifest
  format.
- No fixed node list, no health-tracking subsystem — "online right now"
  is queried fresh at shard time.

## Security Summary

| Concern                         | Mechanism                                             |
|----------------------------------|--------------------------------------------------------|
| Transport encryption            | Already provided by WireGuard (Tailscale/Headscale)    |
| Peer authentication             | Already provided by Tailscale identity (`WhoIs`)       |
| Shard confidentiality           | AES-256-GCM encryption before sharding                 |
| Key distribution                | Never networked — lives only in the local stub file    |
| Tamper detection                | Content-addressed shard IDs (hash = filename); mismatch on fetch = automatic rejection |
| Delete abuse prevention         | Peer-identity check (`WhoIs`) on `chunkd`'s delete handler |
| Access to the mesh itself       | Existing Headscale ACLs (`policy.hujson`)               |

Deliberately not built: PKI, per-user passwords, auth tokens, TLS
termination inside the app (redundant — already inside WireGuard), a
central metadata/auth service.

## Deployment Notes

- Runs on whatever hardware each mesh peer already has — no dedicated
  server VM required for this feature. (Note: an existing single Oracle
  Cloud VM in this setup already runs Headscale itself; that VM is not
  required to run `dstore`/`chunkd` for this design, though it could as
  just another peer if desired.)
- `chunkd` should bind to the peer's Tailscale interface, not a public
  interface.
- No cloud storage account, no S3-compatible bucket, no external API
  keys required for this version of the design (an earlier cloud-backed
  variant was considered and superseded by this peer-storage model).

## Open Decisions for Future Iterations (not required for v1)

- Preferred/trusted peer selection instead of fully random
- Garbage collection when peers go offline permanently
- Re-sharding/repair when shard count drops below a safe threshold
- Concurrency/backpressure during simultaneous distribute operations
- OS-level file association for the stub extension (double-click to
  reconstruct) vs. a CLI/UI trigger
