# Peer-to-peer dstore deployment settings

This documents the active dstore, Docker, Headscale, and restore settings as
verified on 11 September 2026. It describes the deployed configuration; the
repository files and the user-level systemd unit are the sources of truth when
settings need to be changed.

Authentication keys, private Tailscale state, node keys, and other credentials
are intentionally not included here. Keep `.env.peers`, `node.json`, and the
`tailscale/` state directories private.

## Network and hosts

| Host or service | Address | Purpose |
| --- | --- | --- |
| Headscale control server | `https://headscale.withcare.co.za`, public IPv4 `84.8.132.163` | Headscale control plane and shared exit node |
| Headscale exit node | Tailscale `100.64.0.9` (`headscale-server`) | Internet egress for approved users |
| Desktop/origin host | Tailscale `100.64.0.1` (`peter-all-series`) | Origin watcher, local shard server, and restore commands |
| Localmail host | Tailscale `100.64.0.2` | Three additional Docker storage peers |

Headscale is running version `0.28.0`. The exit-node details and recovery
commands are in [headscale-exit-node.md](headscale-exit-node.md).

The dstore peers use TCP port `8080` on their Tailscale addresses. No dstore
ports are published on the public or LAN interfaces. Headscale policy permits
the `personal` dstore share to use port `8080` between the participating
storage nodes.

## Participating dstore nodes

All nodes use share `personal`, role `storage`, `allow_mesh=false`, and the
member list below. The node's own name is appended once more when `dstore init`
creates its persisted configuration; that duplicate is harmless.

```text
peter-all-series
dstore-local-a
dstore-local-b
dstore-local-c
dstore-local-d
dstore-headscale-a
dstore-headscale-b
dstore-localmail-a
dstore-localmail-b
dstore-localmail-c
```

| Node | Tailscale IPv4 | Host | Quota | Maximum file/shard | Port |
| --- | --- | --- | ---: | ---: | ---: |
| `dstore-local-a` | `100.64.0.10` | desktop Docker | 1 GiB | 64 MiB | 8080 |
| `dstore-local-b` | `100.64.0.11` | desktop Docker | 1 GiB | 64 MiB | 8080 |
| `dstore-headscale-a` | `100.64.0.12` | Headscale VM Docker | 1 GiB | 64 MiB | 8080 |
| `dstore-headscale-b` | `100.64.0.13` | Headscale VM Docker | 1 GiB | 64 MiB | 8080 |
| `dstore-localmail-c` | `100.64.0.14` | localmail Docker | 10 GiB | 64 MiB | 8080 |
| `dstore-localmail-a` | `100.64.0.15` | localmail Docker | 10 GiB | 64 MiB | 8080 |
| `dstore-localmail-b` | `100.64.0.16` | localmail Docker | 10 GiB | 64 MiB | 8080 |

The seven dstore peers in the table are the explicit destinations used by the
desktop origin watcher. The generic Headscale and localmail host names are
control or mail services, not shard destinations.

## Desktop/origin watcher

The active service is:

```text
/home/peter/.config/systemd/user/peertopeer-dstore.service
```

Its effective settings are:

```text
WorkingDirectory=/media/peter/storage/PEER_TO_PEER
Binary=/media/peter/storage/PEER_TO_PEER/bin/dstore
Command=agent
Origin=/media/peter/storage/PEER_TO_PEER/outfiles
Local shards=/media/peter/storage/PEER_TO_PEER/.dstore-shards
Listen address=:8080 (bound to Tailscale IPv4 100.64.0.1)
Peers=dstore-local-a=100.64.0.10:8080,
      dstore-local-b=100.64.0.11:8080,
      dstore-headscale-a=100.64.0.12:8080,
      dstore-headscale-b=100.64.0.13:8080,
      dstore-localmail-a=100.64.0.15:8080,
      dstore-localmail-b=100.64.0.16:8080,
      dstore-localmail-c=100.64.0.14:8080
Excluded peers=none
Share=personal
Data shards=5
Parity shards=2
Maximum input file=268435456 bytes (256 MiB)
Plaintext chunk size=16777216 bytes (16 MiB)
Poll interval=2 seconds (the CLI default)
Restart=on-failure
Restart delay=5 seconds
Enabled=yes
Active=yes
```

Place a file in `outfiles/`. After it is stable and all shards upload, dstore
creates `filename.dstore` beside it and removes the original input. The
encrypted local shard copies remain in `.dstore-shards`; remote copies are on
the selected storage peers.

Check or operate the watcher with:

```sh
systemctl --user status peertopeer-dstore.service
systemctl --user restart peertopeer-dstore.service
journalctl --user -u peertopeer-dstore.service -f
```

To change the watched directory, edit the `-origin` value in the service. To
change local shard storage, edit `-shards`. Then reload and restart:

```sh
systemctl --user daemon-reload
systemctl --user restart peertopeer-dstore.service
```

## Restoring files

The desktop association is:

```text
/home/peter/.local/share/applications/peertopeer-dstore-restore.desktop
```

It invokes:

```text
/media/peter/storage/PEER_TO_PEER/bin/dstore-restore-file
```

Double-clicking a `.dstore` manifest restores the original filename into:

```text
/media/peter/storage/PEER_TO_PEER/restored/
```

The default can be changed for the wrapper with `DSTORE_RESTORE_DIR`:

```sh
DSTORE_RESTORE_DIR=/path/to/restore \
  /media/peter/storage/PEER_TO_PEER/bin/dstore-restore-file \
  /path/to/file.dstore
```

For an exact destination, use the binary directly:

```sh
bin/dstore restore \
  -stub outfiles/file.dstore \
  -output /path/to/restored-file
```

## Encryption and shard layout

The current code uses:

```text
Encryption: AES-256-GCM, fresh key per file and fresh nonce per chunk
New files: 5 data shards + 2 parity shards
New files: 7 total shards, with 5 needed for restore
Existing manifests: 2 data shards + 1 parity shard, with 2 needed for restore
```

New files use a version-2 chunked manifest. The watcher reads at most one
16 MiB plaintext chunk at a time, encrypts it with AES-256-GCM, erasure-codes
that ciphertext, uploads the seven shards, and then reuses the buffers for the
next chunk. Restore performs the reverse operation and writes each plaintext
chunk directly to a temporary output file before publishing it atomically.
Memory use therefore stays bounded by the configured chunk and in-flight shard
buffers instead of scaling with the whole file. Existing version-1 manifests
(the old whole-file 2+1 format and the earlier whole-file 5+2 format) remain
readable.

The active watcher still limits new input to 256 MiB. To permit larger files,
raise only the watcher `-max-file-bytes` value after checking disk capacity;
`-chunk-size 16777216` can remain unchanged. With 5+2, local ciphertext copies
use about 1.4 times the plaintext size, in addition to any temporary input or
restored output, so a 4 GiB file needs roughly 5.6 GiB of local shard space.
The desktop currently has about 7.5 GiB free, so its free space should be
checked before raising the limit to 4 GiB.

## Desktop Docker peers

The local desktop peers are defined by:

```text
docker-compose.peers.yml
.env.peers
```

The active `.env.peers` values are:

```dotenv
HEADSCALE_URL=https://headscale.withcare.co.za
HEADSCALE_AUTHKEY=<empty after enrollment>
PEER_PREFIX=dstore-local
DSTORE_MEMBERS=peter-all-series,dstore-local-a,dstore-local-b,dstore-local-c,dstore-local-d,dstore-headscale-a,dstore-headscale-b,dstore-localmail-a,dstore-localmail-b,dstore-localmail-c
DSTORE_SHARE_ID=personal
DSTORE_QUOTA_BYTES=1073741824
DSTORE_MAX_FILE_BYTES=67108864
```

The active desktop services are `peer-a` and `peer-b`. Their persistent state
is under:

```text
./.docker/peers/peer-a/tailscale
./.docker/peers/peer-a/dstore
./.docker/peers/peer-b/tailscale
./.docker/peers/peer-b/dstore
```

They have a 512 MiB memory limit, no CPU limit, `NET_ADMIN` and `NET_RAW`
capabilities, `/dev/net/tun`, `restart: unless-stopped`, a 15-second stop
grace period, and a JSON log limit of three 10 MiB files. The optional
`extra` profile adds `peer-c` and `peer-d`.

Useful commands:

```sh
docker compose --env-file .env.peers -f docker-compose.peers.yml ps
docker compose --env-file .env.peers -f docker-compose.peers.yml up -d
docker compose --env-file .env.peers -f docker-compose.peers.yml down
```

Do not use `down --volumes` unless the persisted Tailscale identities and
shards are intentionally being destroyed.

## Headscale VM Docker peers

The Headscale VM deployment lives under:

```text
/opt/peertopeer-headscale/
```

It uses the base `docker-compose.peers.yml` plus
`docker-compose.headscale.yml`. The deployed image is:

```text
dstore-peer:chunked-20260911
```

The two services are `peer-a` and `peer-b`, with persistent state under the
project's `peers/peer-a/` and `peers/peer-b/` directories. The override limits
each container to:

```text
Memory limit: 192 MiB
Memory reservation: 96 MiB
CPU limit: 0.5
```

The VM has approximately 954 MiB RAM, a 4 GiB `/swapfile`, and a 45 GiB root
filesystem. The active dstore settings are 1 GiB quota, 64 MiB maximum input,
share `personal`, and port `8080`.

Operate this stack over SSH as the `ubuntu` user:

```sh
ssh headscale
cd /opt/peertopeer-headscale
docker compose --env-file .env -f docker-compose.peers.yml \
  -f docker-compose.headscale.yml ps
```

The `.env` file contains the enrollment key and must not be copied into this
document or committed to the repository.

## Localmail Docker peers

The localmail deployment lives under:

```text
/home/peter/peertopeer-localmail/
```

Its Compose file is the repository's `docker-compose.localmail.yml`. The
active `.env.peers` values are:

```dotenv
HEADSCALE_URL=https://headscale.withcare.co.za
HEADSCALE_AUTHKEY=<empty after enrollment>
PEER_PREFIX=dstore-localmail
DSTORE_MEMBERS=peter-all-series,dstore-local-a,dstore-local-b,dstore-local-c,dstore-local-d,dstore-headscale-a,dstore-headscale-b,dstore-localmail-a,dstore-localmail-b,dstore-localmail-c
DSTORE_SHARE_ID=personal
DSTORE_QUOTA_BYTES=10737418240
DSTORE_MAX_FILE_BYTES=67108864
DSTORE_PEER_IMAGE=dstore-peer:chunked-20260911
```

The three services use persistent directories:

```text
/home/peter/peertopeer-localmail/peers/peer-a/tailscale
/home/peter/peertopeer-localmail/peers/peer-a/dstore
/home/peter/peertopeer-localmail/peers/peer-b/tailscale
/home/peter/peertopeer-localmail/peers/peer-b/dstore
/home/peter/peertopeer-localmail/peers/peer-c/tailscale
/home/peter/peertopeer-localmail/peers/peer-c/dstore
```

The external Docker macvlan network is:

```text
Name=dstore-localmail-macvlan
Parent=enp2s0
Subnet=10.0.0.0/24
Gateway=10.0.0.2
IP range=10.0.0.240/28
peer-a=10.0.0.241
peer-b=10.0.0.242
peer-c=10.0.0.243
```

Each localmail peer has a 384 MiB memory limit, 128 MiB reservation, CPU limit
0.75, PID limit 128, the same TUN and Tailscale settings as the other peers,
and a health check every 15 seconds with a 180-second startup period. The
containers use `restart: unless-stopped` and three 10 MiB JSON log files.

The common Docker peer environment is:

```text
TS_AUTH_ONCE=true
TS_USERSPACE=false
TS_ACCEPT_DNS=false
TS_EXTRA_ARGS=--login-server=https://headscale.withcare.co.za
TS_STATE_DIR=/var/lib/tailscale
TS_SOCKET=/var/run/tailscale/tailscaled.sock
DSTORE_SHARE_ID=personal
```

The containers also receive `NET_ADMIN` and `NET_RAW`, mount
`/dev/net/tun`, use DNS `10.0.0.2` and `8.8.8.8`, and pin
`headscale.withcare.co.za` to `84.8.132.163` in `/etc/hosts`.

Useful commands:

```sh
ssh localmail
cd /home/peter/peertopeer-localmail
docker compose --env-file .env.peers -f docker-compose.localmail.yml ps
docker compose --env-file .env.peers -f docker-compose.localmail.yml restart
```

The host currently has about 3.7 GiB RAM, 3.7 GiB swap, and a 116 GiB root
filesystem. Docker's default bridge had no usable outbound path for these
peers, which is why the persistent macvlan network and the pinned
`headscale.withcare.co.za` host entry are part of the active configuration.

## Headscale and exit-node policy

The Headscale policy file is:

```text
/etc/headscale/policy.hujson
```

The dstore deployment adds the `dstore` group and permits the participating
dstore identities to reach one another on TCP port `8080`. The exit-node
configuration separately defines `group:exit-users` for `peter`, `tim`,
`anique`, and `mailservers`, with internet access through
`headscale-server`.

Validate and reload a policy change on the Headscale host:

```sh
sudo headscale policy check --file /etc/headscale/policy.hujson
sudo systemctl reload headscale
```

Do not flush Tailscale-managed firewall chains or replace the server's native
DNS resolver configuration. The exit-node forwarding settings are documented
in [headscale-exit-node.md](headscale-exit-node.md).

## Resource and capacity notes

The desktop host currently has approximately 15 GiB RAM, 4 GiB `/swap.img`,
and 117 GiB storage with about 7.5 GiB free. The root filesystem is close to
full, so monitor it before adding large local shard data. The Headscale VM is
memory constrained even with swap; its dstore peers are intentionally limited
to 192 MiB each. Localmail is the preferred larger storage host, and all peers
accept the 16 MiB chunk shards used by the watcher. Chunking keeps RAM bounded,
but it intentionally moves the working set to disk: the local shard directory
and the remote peers retain encrypted shard copies.

The current application has no automatic size-based routing between local and
remote peers. The desktop watcher now targets the seven explicit dstore peers
listed above.
