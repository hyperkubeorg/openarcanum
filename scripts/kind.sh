#!/usr/bin/env bash
# The local Kubernetes stack on kind (docs/kubernetes.md). Run on your
# computer, not inside a container: it needs rootless podman, kind,
# kubectl and helm there.
#
#   scripts/kind.sh up      create the cluster, build and load the server
#                           images, install the chart (again after changes)
#   scripts/kind.sh proxy   forward the relay and maintainer node to
#                           127.0.0.1:7000 and 127.0.0.1:7100 (Ctrl+C stops)
#   scripts/kind.sh claim   print the community server's claim code
#   scripts/kind.sh status  what's running
#   scripts/kind.sh down    delete the cluster and everything in it
#
# KIND_META_CLUSTER=1 with up: two maintainer nodes in one cluster
# (docs/kubernetes.md "A maintainer cluster"), meta-root and meta-m2, with
# keys and a signed member list made by `meta` inside its image (kept in
# .dev/kind-meta-cluster/); proxy then also forwards meta-m2 to 127.0.0.1:7102.
#   scripts/kind.sh partition   cut the two nodes off from each other
#   scripts/kind.sh heal        end the partition, and bring the root back
#   scripts/kind.sh failover    stop the root node (scale it to 0)
set -euo pipefail
cd "$(dirname "$0")/.."

CLUSTER=${KIND_CLUSTER:-openarcanum}
NAMESPACE=${KIND_NAMESPACE:-openarcanum}
RELEASE=openarcanum
REPO=localhost/openarcanum
# The maintainer cluster's keys and member list (KIND_META_CLUSTER=1).
META_CLUSTER_DIR=.dev/kind-meta-cluster
export KIND_EXPERIMENTAL_PROVIDER=podman

die() { echo "kind.sh: $*" >&2; exit 1; }

need() {
  local missing=()
  for t in "$@"; do command -v "$t" >/dev/null || missing+=("$t"); done
  if ((${#missing[@]})); then
    die "missing on this computer: ${missing[*]}. Install them (docs/kubernetes.md \"What you need\"); this runs on the host, not inside a container."
  fi
}

cluster_exists() { kind get clusters 2>/dev/null | grep -qx "$CLUSTER"; }

kctl() { kubectl --context "kind-$CLUSTER" -n "$NAMESPACE" "$@"; }

cmd_up() {
  need podman kind kubectl helm
  if ! cluster_exists; then
    echo "Creating the kind cluster \"$CLUSTER\" (rootless podman)…"
    # Rootless podman needs the cluster's cgroups delegated to it; on a
    # systemd host that's a user scope with Delegate=yes (kind's docs).
    local wrap=()
    if command -v systemd-run >/dev/null && [[ -d /run/systemd/system ]]; then
      wrap=(systemd-run --user --scope --quiet -p Delegate=yes)
    fi
    "${wrap[@]}" kind create cluster --name "$CLUSTER" --config deploy/kind/cluster.yaml
  fi
  # A new tag per build, so the pods restart with the new images.
  local tag archive
  tag="dev-$(git rev-parse --short HEAD 2>/dev/null || echo local)-$(date +%s)"
  archive=$(mktemp -d)
  trap 'rm -rf "$archive"' RETURN
  for target in relay meta community; do
    echo "Building $REPO-$target:$tag…"
    podman build -q -f deploy/container/Containerfile --ignorefile deploy/container/Containerfile.dockerignore \
      --target "$target" -t "$REPO-$target:$tag" . >/dev/null
    podman save -q -o "$archive/$target.tar" "$REPO-$target:$tag"
    kind load image-archive --name "$CLUSTER" "$archive/$target.tar"
  done
  local extra=()
  if [[ "${KIND_META_CLUSTER:-}" == 1 ]]; then
    meta_cluster_setup "$REPO-meta:$tag"
    extra=(-f "$META_CLUSTER_DIR/values.yaml")
  fi
  echo "Installing the chart…"
  helm upgrade --install "$RELEASE" deploy/helm/openarcanum \
    --kube-context "kind-$CLUSTER" --namespace "$NAMESPACE" --create-namespace \
    -f deploy/helm/openarcanum/values-kind.yaml "${extra[@]}" \
    --set image.tag="$tag" --set image.pullPolicy=Never \
    --wait --timeout 5m
  echo
  cmd_claim || true
  echo
  echo "Next: make kind-proxy (leave it running), then make client-image (once) and make client NAME=alice"
}

# ---------------------------------------------------------------- maintainer cluster

# meta_cluster_values ROOTKEY M2KEY OPERATORKEY: the chart values for the two
# nodes. Clients reach them at 127.0.0.1:7100 and :7102 (proxy), the URLs
# the signed member list names; the nodes reach each other by their
# Services (trusted_peers, which take precedence over the list's URLs).
meta_cluster_values() {
  local root_key=$1 m2_key=$2 op_key=$3
  local svc=".$NAMESPACE.svc.cluster.local:7100"
  cat <<EOF
# Made by scripts/kind.sh (KIND_META_CLUSTER=1). docs/kubernetes.md
meta:
  nodes:
    - name: root
      role: root
      nodeKeySecret: $RELEASE-meta-root-key
      env:
        OPENARCANUM_META_TRUSTED_PEERS: "http://$RELEASE-meta-m2$svc#$m2_key"
        OPENARCANUM_META_TRUSTED_INTERVAL: "1s"
    - name: m2
      role: member
      nodeKeySecret: $RELEASE-meta-m2-key
      env:
        OPENARCANUM_META_TRUSTED_PEERS: "http://$RELEASE-meta-root$svc#$root_key"
        OPENARCANUM_META_TRUSTED_INTERVAL: "1s"
        OPENARCANUM_META_FOLLOW_DECISIONS: "$root_key"
  cluster:
    operatorKey: "$op_key"
    setSecret: $RELEASE-meta-set
EOF
}

# meta_cluster_setup IMAGE: keys for both nodes and the operator (made once,
# kept in META_CLUSTER_DIR), a member list signed with a new seq, the
# Secrets holding them, and the values file.
meta_cluster_setup() {
  local img=$1 dir=$META_CLUSTER_DIR
  mkdir -p "$dir"
  chmod 700 "$dir"
  # `meta` from the image, writing into $dir as this user.
  meta_run() { podman run --rm --userns=keep-id --user "$(id -u):$(id -g)" -v "$PWD/$dir:/work:Z" "$img" "$@"; }
  local n
  for n in root m2; do
    [[ -f "$dir/$n/node.key" ]] || { mkdir -p "$dir/$n"; meta_run id -data "/work/$n" >/dev/null; }
  done
  [[ -f "$dir/op.key" ]] || meta_run operator keygen -out /work/op.key >/dev/null
  local root_key m2_key op_key
  root_key=$(meta_run id -data /work/root | awk '/^node key:/ {print $3}')
  m2_key=$(meta_run id -data /work/m2 | awk '/^node key:/ {print $3}')
  [[ -n "$root_key" && -n "$m2_key" ]] || die "couldn't read the node keys"
  op_key=$(meta_run operator sign-set -key /work/op.key -seq "$(date +%s)" -out /work/set.bin \
    -node "http://127.0.0.1:7100=$root_key:root" -node "http://127.0.0.1:7102=$m2_key" | awk '/^operator key:/ {print $3}')
  [[ -n "$op_key" ]] || die "couldn't sign the member list"
  kubectl --context "kind-$CLUSTER" create namespace "$NAMESPACE" --dry-run=client -o yaml | kubectl --context "kind-$CLUSTER" apply -f - >/dev/null
  for n in root m2; do
    kctl create secret generic "$RELEASE-meta-$n-key" --from-file="node.key=$dir/$n/node.key" \
      --from-file="node.noise=$dir/$n/node.noise" --dry-run=client -o yaml | kctl apply -f - >/dev/null
  done
  kctl create secret generic "$RELEASE-meta-set" --from-file="set.bin=$dir/set.bin" --dry-run=client -o yaml | kctl apply -f - >/dev/null
  meta_cluster_values "$root_key" "$m2_key" "$op_key" >"$dir/values.yaml"
  echo "Maintainer cluster: root $root_key, m2 $m2_key"
  echo "In the app, add the node as http://127.0.0.1:7100#$op_key (or add http://127.0.0.1:7100 and accept its network)."
}

meta_cluster_running() { kctl get deployment "$RELEASE-meta-root" >/dev/null 2>&1; }

cmd_partition() {
  need kubectl
  meta_cluster_running || die "no maintainer cluster: run KIND_META_CLUSTER=1 make kind-up first"
  # Maintainer nodes take no traffic from each other; everything else
  # (community server, relay, the proxy's port-forwards) still reaches them.
  kctl apply -f - <<EOF
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: $RELEASE-meta-partition
spec:
  podSelector:
    matchLabels: {app.kubernetes.io/component: meta}
  policyTypes: [Ingress]
  ingress:
    - from:
        - podSelector:
            matchExpressions:
              - {key: app.kubernetes.io/component, operator: NotIn, values: [meta]}
EOF
  echo "The maintainer nodes can't reach each other now; make kind-heal ends it."
}

cmd_heal() {
  need kubectl
  meta_cluster_running || die "no maintainer cluster: run KIND_META_CLUSTER=1 make kind-up first"
  kctl delete networkpolicy "$RELEASE-meta-partition" --ignore-not-found
  kctl scale deployment "$RELEASE-meta-root" --replicas=1
  echo "Partition ended and the root node running; they catch up within seconds."
}

cmd_failover() {
  need kubectl
  meta_cluster_running || die "no maintainer cluster: run KIND_META_CLUSTER=1 make kind-up first"
  kctl scale deployment "$RELEASE-meta-root" --replicas=0
  echo "The root node is stopped: apps go to meta-m2 (127.0.0.1:7102). make kind-heal starts it again."
}

cmd_proxy() {
  need kubectl
  cluster_exists || die "no cluster \"$CLUSTER\": run make kind-up first"
  echo "Relay:           ws://127.0.0.1:7000/ws"
  echo "Maintainer node: http://127.0.0.1:7100"
  echo "(call media goes to 127.0.0.1:7000/udp through kind's port mapping)"
  echo "Ctrl+C to stop."
  local pids=()
  trap 'kill "${pids[@]}" 2>/dev/null || true' EXIT INT TERM
  # Restarted if a pod restarts (a port-forward ends with its pod).
  forward() {
    while true; do
      kctl port-forward --address 127.0.0.1 "svc/$RELEASE-$1" "$2:${3:-$2}" >/dev/null || true
      sleep 1
    done
  }
  forward relay 7000 & pids+=($!)
  if meta_cluster_running; then
    echo "Maintainer node m2: http://127.0.0.1:7102"
    forward meta-root 7100 & pids+=($!)
    forward meta-m2 7102 7100 & pids+=($!)
  else
    forward meta 7100 & pids+=($!)
  fi
  wait
}

cmd_claim() {
  need kubectl
  local out
  out=$(kctl logs "$RELEASE-community-0" -c init 2>/dev/null) || die "the community server isn't running yet"
  if grep -q '^openarcanum-claim:' <<<"$out"; then
    echo "Claim code (in the app: + → Host on your own server; it works once, so if"
    echo "this server was claimed already, it no longer does):"
    grep '^openarcanum-claim:' <<<"$out" | tail -1
  else
    echo "No claim code to show: the server has been claimed (it deletes its claim file then)."
  fi
}

cmd_status() {
  need kubectl
  cluster_exists || { echo "No cluster \"$CLUSTER\"."; return; }
  kctl get pods,svc,pvc
}

cmd_down() {
  need kind
  if cluster_exists; then
    kind delete cluster --name "$CLUSTER"
  else
    echo "No cluster \"$CLUSTER\"."
  fi
}

case "${1:-}" in
  up) cmd_up ;;
  proxy) cmd_proxy ;;
  claim) cmd_claim ;;
  status) cmd_status ;;
  down) cmd_down ;;
  partition) cmd_partition ;;
  heal) cmd_heal ;;
  failover) cmd_failover ;;
  # For tests (clients/desktop/tests/test_helm.py): the cluster values for given keys.
  cluster-values) shift; meta_cluster_values "$@" ;;
  *) sed -n '2,21p' "$0" | sed 's/^# \{0,1\}//'; exit 2 ;;
esac
