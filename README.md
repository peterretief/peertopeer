# Distributed Store

Peer-to-peer file storage for an existing Headscale/Tailscale mesh.

The design is documented in [AGENT.md](AGENT.md). In short, each peer runs a local storage daemon and an origin-folder watcher. Files are encrypted, erasure-coded into 4 data shards plus 2 parity shards, and distributed across six online mesh peers. The local file is replaced by a small stub containing the manifest needed to reconstruct it.

## Status

Design stage. The first implementation milestone should cover:

- AES-256-GCM encryption before erasure coding.
- Content-addressed shard storage in `chunkd`.
- Local stub manifest creation and reconstruction.
- Tailscale identity checks for write/delete behavior.

## Non-goals for v1

- Public sharing outside the mesh.
- Central metadata or auth services.
- Cloud object storage.
- Advanced peer preference, repair, or health tracking.

## Testing

Run the current local scaffold with:

```sh
go test ./...
```

The test covers encrypting file bytes, splitting ciphertext into 4+2 Reed-Solomon shards, storing shards by content hash, loading a stub manifest, reconstructing from 4 shards, and decrypting back to the original bytes.
