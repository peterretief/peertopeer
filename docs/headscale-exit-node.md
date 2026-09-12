# Shared Headscale exit node

Configured and verified on 11 September 2026 for
`https://headscale.withcare.co.za`.

| Setting | Value |
| --- | --- |
| Exit-node name | `headscale-server` |
| Tailscale IPv4 | `100.64.0.9` |
| MagicDNS name | `headscale-server.mesh.withcare.co.za` |
| Headscale node ID | `12` |
| Public internet IPv4 | `84.8.132.163` |
| Permitted accounts | `peter`, `tim`, `anique`, `mailservers` |
| Versions at setup | Headscale `0.28.0`, server Tailscale `1.98.4` |

This reuses the existing Oracle VM. One shared exit node serves the existing
accounts; no additional VM or user identity was created. Each client selects
the exit node individually. The setup does not automatically switch users'
devices to it.

## Use it

In the Tailscale app on a phone or desktop, open the exit-node selector and
choose **headscale-server**. Select **None** to return to the normal connection.
See the [Tailscale exit-node instructions](https://tailscale.com/docs/features/exit-nodes)
for platform-specific steps.

On Linux:

```sh
sudo tailscale set --exit-node=100.64.0.9
```

To retain access to the client's own local network while using the exit node:

```sh
sudo tailscale set --exit-node=100.64.0.9 --exit-node-allow-lan-access=true
```

To stop using it:

```sh
sudo tailscale set --exit-node=
```

List available exit nodes with `tailscale exit-node list`. While using this
exit node, `curl -4 https://api.ipify.org` should return `84.8.132.163`.

## Server configuration

The existing Tailscale identity was offline because `/etc/resolv.conf` pointed
to Tailscale DNS while Tailscale needed DNS to reach its Headscale server.
The repair disabled DNS acceptance on this server using
`tailscale set --accept-dns=false`, restored `/etc/resolv.conf` as a symlink to
`/run/systemd/resolve/resolv.conf`, and restarted `systemd-resolved`. The native
DNS resolver supplied on `ens3` is `169.254.169.254`.

Keep this server's DNS independent of its own Tailscale connection. Clients
can continue using Tailscale DNS normally; that path was tested too.

`/etc/sysctl.d/99-tailscale-exit.conf` persists:

```ini
net.ipv4.ip_forward = 1
net.ipv6.conf.all.forwarding = 1
```

The server advertises exit routing using `tailscale set --advertise-exit-node`.
Headscale approves both default routes on node 12:

```sh
sudo headscale nodes approve-routes --identifier 12 --routes '0.0.0.0/0,::/0'
```

Tailscale manages the `ts-forward` and `ts-postrouting` firewall chains,
including masquerading. Its forwarding rule precedes the server's existing
reject rules. Do not flush the firewall or save transient Docker/Tailscale
chains as a replacement firewall configuration.

The original groups, host aliases, and three device-access ACL rules remain
unchanged. `/etc/headscale/policy.hujson` adds this group:

```json
"group:exit-users": ["peter@", "tim@", "anique@", "mailservers@"]
```

and this ACL:

```json
{
  "action": "accept",
  "src": ["group:exit-users"],
  "dst": ["autogroup:internet:*"]
}
```

Future accounts need inclusion in `group:exit-users` to receive this permission.
Validate policy edits with `sudo headscale policy check --file
/etc/headscale/policy.hujson`, then apply them with
`sudo systemctl reload headscale`. This uses Headscale's documented
[exit-node approval and access control](https://headscale.net/0.28.0/ref/routes/).

## Verification performed

- Headscale reported both default routes approved, available, and serving.
- A local container used the exit node and reached HTTPS services with public
  IP `84.8.132.163`. Its normal IP was `102.182.240.22`.
- UDP DNS, ICMP, and Headscale's HTTPS health endpoint worked through the exit.
- Normal Tailscale DNS via `100.100.100.100` and HTTPS worked together while
  selecting the exit node.
- The container's original exit-node and DNS preferences were restored.
- A `mailservers` client independently reported the exit node as available.
- The active packet filter permitted internet HTTPS for representative nodes
  from all four accounts. Tim, Anique, and mailservers did not gain SSH access
  to the exit node, Oracle private-network access, or cloud metadata access.
- A Tailscale service restart preserved its identity, native DNS, exit
  advertising, forwarding/NAT rules, and healthy connection. The services are
  enabled at boot. A full VM reboot was not performed.

## Limits and recovery

The VM currently has no public IPv6 address or IPv6 default route. IPv4 internet
routing is verified; IPv6-only internet destinations are unavailable through
this exit node despite the paired IPv6 route advertisement.

There is one exit host, sharing resources with Headscale and other existing
services. Throughput, concurrent-user capacity, and the cloud account's
bandwidth allowance were not measured. Another independent host would be
needed for a second egress location or redundancy.

Root-only backups on the server are in:

```text
/root/headscale-exit-backup-20260911T092848Z/
```

They contain the original resolver file, ACL policy, Tailscale state/preferences,
firewall rules, and forwarding values. The identity backup is sensitive.

To withdraw this exit node, SSH to `headscale`, then run:

```sh
sudo headscale nodes approve-routes --identifier 12 --routes ''
sudo tailscale set --advertise-exit-node=false
```

Clients using it should select **None**. If retiring the feature, remove only
the added internet ACL and `group:exit-users`, validate, and reload Headscale.
Preserve the DNS repair. Avoid restoring the entire old policy over later
changes, and preserve IPv4 forwarding already used by the server's other
network services.
