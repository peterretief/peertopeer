# Docker peers on a Headscale host

`docker-compose.peers.yml` runs two independent dstore peers by default on one
Linux machine that already has a reachable Headscale server. The optional
Compose profile `extra` adds `peer-c` and `peer-d`. The Headscale server itself
remains unchanged; each service registers as a separate Tailscale node.

The desktop watcher currently uses five data shards plus two parity shards.
The two default containers are storage contributors; the explicit desktop
watcher supplies seven destinations. A standalone `dstore node` keeps the
legacy 2+1 default; the desktop watcher is explicitly configured for 5+2.

## Before starting

Create a Headscale user and a reusable pre-authentication key. Run these on the
Headscale host, adjusting the user name and expiry to your policy:

```sh
headscale users create dstore
headscale preauthkeys create --user dstore --reusable --expiration 24h
```

If Headscale is itself in Docker, prefix both commands with
`docker exec -it <headscale-container>`. Keep the resulting key private.

The peer host must be Linux and expose `/dev/net/tun` to Docker. No public port
mapping is needed: dstore listens on each container's Tailscale interface.
Ensure the Headscale policy and any host firewall allow TCP port 8080 between
the peer nodes.

## Start the peers

From the repository root:

```sh
cp .env.peers.example .env.peers
# Edit .env.peers: set HEADSCALE_URL and HEADSCALE_AUTHKEY.
docker compose --env-file .env.peers -f docker-compose.peers.yml up -d --build
```

Use `--profile extra` when all four local peers are needed:

```sh
docker compose --env-file .env.peers -f docker-compose.peers.yml --profile extra up -d --build
```

`HEADSCALE_URL` must be reachable from inside the containers; `localhost` only
works when Headscale is bound inside the same container network namespace.
Each peer has persistent state under `.docker/peers/`, so recreating a service
does not register a new node. The same reusable key may be used during initial
bootstrap because every service has its own persisted Tailscale state.

Check registration and dstore readiness with:

```sh
docker compose --env-file .env.peers -f docker-compose.peers.yml ps
docker compose --env-file .env.peers -f docker-compose.peers.yml exec peer-a tailscale ip -4
docker compose --env-file .env.peers -f docker-compose.peers.yml exec peer-a dstore peers
```

Drop a file into any peer's origin directory to exercise placement:

```sh
cp ./example.txt .docker/peers/peer-a/dstore/outfiles/
```

The resulting `.dstore` manifest and local shard data stay in that peer's
persisted directory. Stop the stack with:

```sh
docker compose --env-file .env.peers -f docker-compose.peers.yml down
```

Omit `--volumes` if the peer identities and shard data should be retained.

## Configuration knobs

The Compose file creates each peer's `node.json` on first start. `dstore node`
enforces the sharing group, quota, and member identity list from
`DSTORE_MEMBERS`; update that list before enrollment. Automatic placement still
discovers online compatible members. Set any of these in `.env.peers` if needed:

```dotenv
DSTORE_MAX_FILE_BYTES=4294967296
DSTORE_INTERVAL=5s
```

The container settings follow the [Tailscale Docker configuration
parameters](https://tailscale.com/docs/features/containers/docker/docker-params)
and [Headscale pre-authenticated registration
flow](https://headscale.net/stable/usage/getting-started/).
