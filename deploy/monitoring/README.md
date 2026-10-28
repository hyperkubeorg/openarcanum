# Monitoring the network

Prometheus scrapes each machine's status listener; Grafana shows the
dashboard here and alerts on the rules here. The metrics answer two
questions: **are people using the service, and how much** (to know when to
add relays), and **is it healthy**.

## What the metrics are, and aren't

Counts, sizes and rates only. Nothing in them identifies a person, member,
device, community or message, and nothing carries content: everything that
matters is end-to-end encrypted, and the metrics come from what each
server already handles as ciphertext.

| Question | Metric (source) |
|---|---|
| Communities in use | `openarcanum_meta_communities_active_{1h,24h,7d}` (community servers' heartbeats, maintainer) |
| People online for direct messages | the rate of `openarcanum_meta_http_requests_total{route="POST /v1/mailbox/fetch"}` (open clients read each of their inboxes every 5 seconds: a rough measure); `openarcanum_meta_mailbox_watches` once clients use mailbox watches |
| Direct messages sent | `openarcanum_meta_http_requests_total{route="POST /v1/mailbox/drop"}` |
| Relay load, network-wide | `openarcanum_meta_relay_connections` and `openarcanum_meta_relay_capacity` (the relays' heartbeats, summed) |
| Each relay | `openarcanum_relay_connections`, `_calls`, `_call_participants`, `_delivered_bytes_total`, `_media_sent_bytes_total` |
| Machine load | `process_cpu_seconds_total`, `process_resident_memory_bytes` (every node) |
| Health | `up`, `openarcanum_meta_relays_alive`, `openarcanum_community_unready`, request status codes |

The full list per node is in docs/operations.md ("Metrics").

## Setting up the monitoring server

1. Run Prometheus and Grafana on it (any install). Copy `prometheus.yml`
   and `alerts.yml` into Prometheus's configuration directory.
2. On each machine to monitor, run its setup with this server's address
   (an IP address, or a network):

   ```sh
   cd /root/openarcanum-podman/<machine>
   MONITOR_FROM=203.0.113.7 bash setup.sh
   ```

   That puts the node's status listener on the network, makes a token
   that `/metrics` requires (setup.sh prints where it is), and opens the
   port in the firewall to that address only. The choice is kept: later
   runs leave it on. `MONITOR_FROM=off bash setup.sh` turns it off again.
3. Copy each machine's token into `tokens/<machine>` next to
   `prometheus.yml` (`chmod 600`, readable by Prometheus only), named as
   in `prometheus.yml`.
4. In Grafana, add the Prometheus data source and import
   `grafana-dashboard.json`.

| Machine kind | Port |
|---|---|
| maintainer node | 7100/tcp |
| relay | 7000/tcp (its media is 7000/udp) |
| website | 7301/tcp |
| community server | 7201/tcp |

`/healthz` and `/readyz` answer without the token (they say "ok" or why
not, nothing else); `/metrics` needs it.

## Alerts

`alerts.yml` alerts on a node not answering, fewer than two relays alive, a
community with no relay, maintainer server errors or cluster lag, failed
community backups, and on capacity: relays above 70% of their connection
capacity, refusing connections, or using most of a CPU. Those three are the
cue to add a relay. The thresholds are starting points.
