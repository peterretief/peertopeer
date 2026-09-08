# Distributed Store

Peer-to-peer file storage for an existing Headscale/Tailscale mesh.

The design is documented in [AGENT.md](AGENT.md). The current scaffold encrypts a file, splits the ciphertext into 4 data shards plus 2 parity shards, stores those shards by content hash, and writes a portable `.dstore` manifest that can restore the original file.

Important: the `.dstore` manifest contains the AES key. Anyone who receives the manifest and can reach at least 4 shard URLs can recreate the original file.

## Local Workflow

Start a shard server:

```sh
go run ./cmd/dstore serve-shards -shards .dstore-shards -addr :8080
```

In another terminal, watch the `outfiles` folder:

```sh
go run ./cmd/dstore watch -origin outfiles -shards .dstore-shards
```

Drop a regular file into `outfiles`. The watcher will:

- Encrypt the file.
- Split it into 6 Reed-Solomon shards.
- Store the shards in `.dstore-shards` by SHA-256 hash.
- Create `filename.dstore` in `outfiles`.
- Remove the original file from `outfiles`.

You can email the `.dstore` file. It includes shard download URLs like:

```text
http://100.x.y.z:8080/shards/{hash}
```

By default, `process` and `watch` use `tailscale ip -4` to embed this node's Headscale/Tailscale IPv4 address in the manifest. Pass `-base-url` only when you want to override that URL, for example to use MagicDNS.

Restore from a manifest:

```sh
go run ./cmd/dstore restore -stub outfiles/example.txt.dstore
```

The restore command downloads shards from the URLs in the manifest. It only needs 4 of the 6 shards to be reachable.

## One-shot Processing

Instead of running the watcher, process the current files in `outfiles` once:

```sh
go run ./cmd/dstore process -origin outfiles -shards .dstore-shards
```

## Testing

```sh
go test ./...
```

The tests cover local reconstruction and portable manifest restoration over HTTP without a local shard directory fallback.

## Service Management

Install the user-level systemd service:

```sh
./scripts/install-user-service.sh
```

The installer builds `bin/dstore`, writes `~/.config/systemd/user/peertopeer-dstore.service`, and reloads the user systemd manager. It does not start the service automatically.

Start the combined shard server and `outfiles` watcher:

```sh
systemctl --user start peertopeer-dstore
```

Check status:

```sh
systemctl --user status peertopeer-dstore
```

Follow logs:

```sh
journalctl --user -u peertopeer-dstore -f
```

Stop the service:

```sh
systemctl --user stop peertopeer-dstore
```

Start it automatically when your user session starts:

```sh
systemctl --user enable peertopeer-dstore
```

The service runs:

```sh
bin/dstore agent -origin outfiles -shards .dstore-shards -addr :8080
```

That single process serves shard downloads and watches `outfiles` for new files.
