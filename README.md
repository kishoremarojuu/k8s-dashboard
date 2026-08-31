# Kubernetes Troubleshooter

A focused, local Kubernetes troubleshooting and capacity-diagnosis tool. It
connects through your existing kubeconfig, serves the dashboard from one Go
process, and does not install anything into the cluster.

The product is intentionally narrower than a general Kubernetes resource
browser. Its primary workflows are:

- compare scheduled CPU/memory reservations with actual runtime usage;
- move between workloads, pods, nodes, events, logs, and owner relationships;
- identify CrashLoopBackOff, OOM, scheduling, probe, and rollout failures;
- keep destructive actions disabled unless the operator explicitly enables
  the small write allowlist.

## Current milestone

The first Go-backed vertical slice is runnable. The Go process now:

- loads the selected kubeconfig context with `client-go`;
- serves the existing HTML dashboard as an embedded asset;
- exposes product APIs for health, contexts, namespaces, nodes, and local
  runtime configuration;
- provides a narrow compatibility gateway for the Kubernetes resources the
  existing dashboard currently reads;
- blocks Secrets and arbitrary Kubernetes API access;
- blocks writes by default;
- validates Host and Origin headers and binds only to loopback;
- optionally tails explicitly allowlisted container file paths through the
  Kubernetes exec API, replacing the Python helper.

The compatibility gateway lets the current dashboard keep working while each
screen is migrated to normalized product-level APIs.

## Run it

Requirements:

- Go 1.24 or newer;
- a working kubeconfig and Kubernetes context;
- metrics-server for actual CPU/memory usage (optional).

```bash
./start.sh
```

The dashboard opens at `http://127.0.0.1:7777` and connects automatically.
Use `--no-open` during development or automation:

```bash
./start.sh --no-open
```

Choose another kubeconfig context:

```bash
./start.sh --context minikube
./start.sh --kubeconfig /path/to/config --context staging
```

The server refuses non-loopback listen addresses.

## Write actions

The default mode is read-only. Enable the current delete/restart-style actions
only for a deliberate session:

```bash
./start.sh --write
```

Even in write mode, the compatibility gateway only permits deletion of a
specific Pod, Deployment, or DaemonSet. Other methods and resources remain
blocked, and Kubernetes RBAC is still authoritative.

## Optional file-backed logs

Kubernetes normally exposes container stdout/stderr. If an application writes
logs to a file inside its container, allow each exact path when starting the
tool:

```bash
./start.sh \
  --file-log-path /var/log/my-app/application.log \
  --file-log-path /var/log/my-app/audit.log
```

Only the configured paths appear in the UI. The backend executes a fixed
`tail -n <lines> -- <path>` argument vector through the Kubernetes exec API;
the browser cannot provide an arbitrary command. This capability requires
`pods/exec` permission and is disabled when no paths are configured.

## Product API

The normalized local API currently includes:

| Endpoint | Purpose |
| --- | --- |
| `GET /api/product/v1/health` | Tool version, context, Kubernetes connectivity |
| `GET /api/product/v1/config` | Safe UI configuration and enabled capabilities |
| `GET /api/product/v1/contexts` | Available and active kubeconfig contexts |
| `GET /api/product/v1/namespaces` | Visible namespace names |
| `GET /api/product/v1/nodes` | Normalized node health and capacity summary |
| `GET /api/product/v1/file-logs` | Tail an explicitly allowlisted in-container file |

The dashboard still uses a narrow set of raw-shaped compatibility routes for
pods, events, apps workloads, metrics, and stdout logs. Those routes are an
incremental migration mechanism, not the long-term frontend contract.

## Development

```bash
cd kube-troubleshooter
cp ../k8s-dashboard.html k8s-dashboard.html
go test ./...
go build ./...
go run . --no-open
```

The copied HTML file is ignored by Git. Running the root `./start.sh` performs
the synchronization automatically before compiling the embedded dashboard.

The legacy `log_file_server.py` remains in the repository temporarily for
comparison, but the new launcher does not use Python or `kubectl proxy`.

## Near-term roadmap

1. Replace raw-shaped compatibility calls with normalized workload, pod,
   metrics, event, and log APIs.
2. Make owner references and pod placement a reusable relationship graph.
3. Add a guided incident workspace that keeps evidence and navigation context
   together.
4. Add watch/SSE streams so the UI updates from Kubernetes watches instead of
   polling.
5. Introduce reproducible failure scenarios and measure diagnosis time, steps,
   and accuracy against conventional workflows.
