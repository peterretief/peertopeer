# Distributed Store

Peer-to-peer file storage for an existing Headscale/Tailscale mesh.

The design is documented in [AGENT.md](AGENT.md). The deployed desktop watcher encrypts files, splits each chunk into 5 data shards plus 2 parity shards, distributes those shards across online peers, and writes a portable `.dstore` manifest that can restore the original file. New files are processed in bounded 16 MiB chunks so memory use does not grow with the file size; standalone commands retain the legacy 2+1 defaults unless their layout flags are set.

Important: the `.dstore` manifest contains the AES key. Anyone who receives the manifest and can reach at least 5 shard URLs for every version-2 chunk can recreate the original file.

## Prerequisites

- Go 1.24+
- Tailscale or Headscale running on each peer
- Each peer must have a unique hostname configured in Tailscale

## Quick Start

Build the binary:

```sh
go build -o bin/dstore ./cmd/dstore
```

Run the agent (serves shards + watches for new files):

```sh
bin/dstore agent -origin outfiles -shards .dstore-shards -addr :8080
```

To use specific shard nodes instead of random online Tailscale peers, pass `-peers`:

```sh
bin/dstore agent -origin outfiles -shards .dstore-shards -addr :8080 \
  -peers node-a=100.64.0.10:8080,node-b=100.64.0.11:8080,node-c=100.64.0.12:8080
```

The peer format is `hostname=host[:port]`. If fewer peers are listed than shard count, shards are assigned round-robin across those peers.

Drop a file or directory into `outfiles/`. The agent will process regular files recursively and leave `.dstore` manifests beside each original path:

1. Encrypt it with AES-256-GCM
2. Split each encrypted chunk into 7 Reed-Solomon shards (5 data + 2 parity)
3. Randomly select online peers from your Tailscale mesh, or use the explicit `-peers` list
4. Push each shard to a peer via HTTP PUT
5. Store a local copy as well
6. Create `filename.dstore` in `outfiles/`
7. Remove the original file

## Peer-to-Peer Workflow

### On each mesh peer

Install and start the agent:

```sh
./scripts/install-user-service.sh
systemctl --user start peertopeer-dstore
systemctl --user enable peertopeer-dstore
```

Or run directly:

```sh
bin/dstore agent -origin outfiles -shards .dstore-shards -addr :8080
```

### Storing a file

Drop any file into the `outfiles/` directory on any peer. The agent automatically:

- Picks random online peers from your Tailscale mesh, or uses the configured `-peers` shard nodes
- Pushes shards to each peer's shard server
- Creates a `.dstore` manifest in `outfiles/`

### Restoring a file

**Double-click** (after desktop integration):

```sh
./scripts/install-desktop-integration.sh
xdg-open outfiles/example.txt.dstore
```

**CLI:**

```sh
bin/dstore restore -stub outfiles/example.txt.dstore
```

Restore fetches shards from the URLs in the manifest. Only 5 of 7 shards per chunk need to be reachable. If a peer is offline, the remaining shards are sufficient.

### Sharing a file

Email the `.dstore` file to another mesh member. They can restore it on any peer that can reach at least 5 of the 7 shard URLs for every chunk.

## Commands

| Command | Description |
|---------|-------------|
| `peers` | List Tailscale peers visible to this node |
| `agent` | Combined shard server + file watcher (recommended) |
| `watch` | Watch a directory for new files to shard |
| `process` | One-shot: process all current files in a directory |
| `restore` | Restore a file from a `.dstore` manifest |
| `serve-shards` | Run only the shard HTTP server |

### peers

```sh
bin/dstore peers
bin/dstore peers -online-only
```

### agent

```sh
bin/dstore agent \
  -origin outfiles \
  -shards .dstore-shards \
  -addr :8080 \
  -base-url "" \
  -interval 2s \
  -peers node-a=100.64.0.10:8080,node-b=100.64.0.11:8080
```

| Flag | Default | Description |
|------|---------|-------------|
| `-origin` | `outfiles` | Directory to watch for files to shard |
| `-shards` | `.dstore-shards` | Directory for local shard storage |
| `-addr` | `:8080` | HTTP listen address |
| `-base-url` | auto | Override URL in manifests (default: Tailscale IPv4) |
| `-interval` | `2s` | Watch poll interval |
| `-peers` | auto | Comma-separated explicit shard nodes as `hostname=host[:port]`; overrides random peer selection |
| `-peer-ports` | none | Comma-separated `hostname:port` overrides for auto-selected peers |
| `-data-shards` | `2` | Number of data shards (the active service passes `5`) |
| `-parity-shards` | `1` | Number of parity shards (the active service passes `2`) |
| `-chunk-size` | `16 MiB` | Plaintext bytes processed at a time |
| `-max-file-bytes` | `64 MiB` | Maximum input file size for this command |

### restore

```sh
bin/dstore restore \
  -stub outfiles/photo.dstore \
  -shards .dstore-shards \
  -output photo.jpg
```

| Flag | Default | Description |
|------|---------|-------------|
| `-stub` | (required) | Path to the `.dstore` manifest |
| `-shards` | (none) | Local shard directory fallback |
| `-output` | (stub path minus `.dstore`) | Output path for restored file |

## Service Management

```sh
systemctl --user start peertopeer-dstore    # start
systemctl --user stop peertopeer-dstore     # stop
systemctl --user status peertopeer-dstore   # status
systemctl --user enable peertopeer-dstore   # auto-start on login
journalctl --user -u peertopeer-dstore -f   # follow logs
```

## Docker peers on the Headscale server

To run multiple storage peers on the same Linux machine as an existing
Headscale server, use the included Compose stack. It creates two separate
Tailscale nodes (`peer-a` and `peer-b`) by default with persistent identities
and storage; `--profile extra` adds `peer-c` and `peer-d`. No public dstore
ports are exposed.

The current erasure layout needs three ready storage peers for automatic file
placement, so enable the extra profile or add a remote dstore node before using
a peer as a file origin.

Create a reusable Headscale pre-authentication key, copy the environment
template, and start the stack:

```sh
headscale users create dstore
headscale preauthkeys create --user dstore --reusable --expiration 24h
cp .env.peers.example .env.peers
# Set HEADSCALE_URL and HEADSCALE_AUTHKEY in .env.peers.
docker compose --env-file .env.peers -f docker-compose.peers.yml up -d --build
```

See [docs/docker-peers.md](docs/docker-peers.md) for verification, storage
paths, and the required `/dev/net/tun` Docker capability.

## Desktop Integration

Install `.dstore` file association:

```sh
./scripts/install-desktop-integration.sh
```

After installation, double-clicking a `.dstore` file restores the original file.

## Security

| Concern | Mechanism |
|---------|-----------|
| Transport encryption | WireGuard (Tailscale/Headscale) |
| Peer authentication | Tailscale identity (`tailscale whois`) |
| Shard confidentiality | AES-256-GCM before sharding |
| Key distribution | Lives only in the `.dstore` manifest |
| Tamper detection | Content-addressed shard IDs (hash = filename) |
| Delete abuse prevention | Only the original writer can delete a shard |

## Testing

```sh
go test ./...
```
