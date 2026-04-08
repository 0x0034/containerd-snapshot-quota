# Containerd Snapshot Quota

[![Go Version](https://img.shields.io/github/go-mod/go-version/0x0034/containerd-snapshot-quota)](go.mod)
[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)

A lightweight daemon that enforces per-container disk quotas on XFS filesystems via project quota, integrated with the containerd runtime. It transparently limits how much disk space each container's writable layer can consume — without modifying the container image, runtime config, or affecting normal container operation.

## How It Works

```
                        containerd
                            │
               TaskCreate / TaskDelete events
                            │
                    ┌───────▼────────┐
                    │  Quota Agent   │
                    │                │
                    │  1. Extract overlay upperdir/workdir from /proc/<pid>/mountinfo
                    │  2. Allocate XFS Project ID
                    │  3. Bind project ID to directory (xfs_quota project -s)
                    │  4. Set block limit (xfs_quota limit -p)
                    │  5. Persist state to Badger DB
                    └───────┬────────┘
                            │
               XFS kernel enforces quota
                            │
                    Container sees ENOSPC
                    when limit is reached
```

**Key properties:**

- Quota is enforced at the kernel level — zero overhead at runtime
- Containers are unaware of the quota; they simply receive `ENOSPC` when the limit is hit, identical to a full disk
- Works with any OCI runtime that uses overlayfs (Docker, nerdctl, Kubernetes CRI)
- Survives agent restarts — state is recovered from persistent storage

## Features

- **Event-driven** — subscribes to containerd `TaskCreate` / `TaskDelete` events, no polling
- **Rule matching** — apply different quota sizes by container name, image, or Kubernetes pod metadata
- **Auto quota** — optionally apply a default quota to all containers with no rules needed
- **Prometheus metrics** — real-time per-container disk usage, limits, and usage ratio at `/metrics`
- **HTTP API** — list and clean quota records via REST
- **State recovery** — automatic re-apply on restart; stale records are garbage-collected
- **Multi-namespace** — monitor `default`, `moby`, `k8s.io`, or any custom containerd namespace

## Requirements

| Requirement | Details |
|---|---|
| OS | Linux |
| Filesystem | XFS with `prjquota` mount option |
| Runtime | containerd 1.6+ |
| Tools | `xfs_quota`, `xfs_io` (provided by `xfsprogs`) |

### Enable XFS Project Quota

```bash
# Remount with prjquota (immediate, non-persistent)
mount -o remount,prjquota /var/lib/containerd

# Persistent — add prjquota to /etc/fstab
# /dev/sda1  /var/lib/containerd  xfs  defaults,prjquota  0 0
```

Verify:

```bash
xfs_quota -x -c "state" /var/lib/containerd
# Should show: Accounting: ON, Enforcement: ON
```

## Quick Start

### Build

```bash
git clone https://github.com/0x0034/containerd-snapshot-quota.git
cd containerd-snapshot-quota
go build -o quota-agent ./cmd/main.go
```

### Configure

```bash
mkdir -p /etc/quota-agent
cp configs/config.yaml /etc/quota-agent/config.yaml
```

Edit `/etc/quota-agent/config.yaml`:

```yaml
version: v1
persistDir: /var/lib/quota-agent

manager:
  socket: /run/containerd/containerd.sock
  mountPoint: /var/lib/containerd    # must be on an XFS partition
  size: "10G"                        # default quota per container
  autoQuota: true
  namespaces:
    - k8s.io

web:
  enabled: true
  port: 8080
```

### Run

```bash
./quota-agent -config /etc/quota-agent/config.yaml
```

### Deploy as systemd Service

```bash
cp quota-agent /usr/local/bin/

cat > /etc/systemd/system/quota-agent.service << 'EOF'
[Unit]
Description=Containerd Snapshot Quota Agent
After=containerd.service
Requires=containerd.service

[Service]
Type=simple
ExecStart=/usr/local/bin/quota-agent -config /etc/quota-agent/config.yaml -logtostderr -v 2
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable --now quota-agent
```

## Configuration

### Operating Modes

| Mode | `autoQuota` | `matchRules` | Behavior |
|---|---|---|---|
| **OnlyAuto** | `true` | empty | All containers get the default quota |
| **OnlyRules** | `false` | defined | Only matched containers get quota |
| **All** | `true` | defined | Matched containers use rule quota; others get the default |

### Match Rules

Rules are evaluated in order; the first match wins.

```yaml
manager:
  matchRules:
    # By image name
    - name: databases
      size: "100G"
      level: image
      selector:
        name: mysql

    # By Kubernetes pod
    - name: prod-api
      size: "50G"
      level: pod
      selector:
        name: api-server
        namespace: production

    # By container name (substring match)
    - name: log-collectors
      size: "30G"
      level: container
      selector:
        name: filebeat
```

### Environment Variable Overrides

All config values can be overridden with `QUOTA_` prefixed env vars:

```bash
export QUOTA_MANAGER_SIZE=50G
export QUOTA_MANAGER_MOUNTPOINT=/data/containerd
export QUOTA_WEB_ENABLED=false
```

## API

### `GET /health`

```json
{"status": "ok"}
```

### `GET /api/quotas?page=1&pageSize=20`

Returns paginated quota records with container metadata and status.

### `POST /api/quotas/clean`

Removes all quota records and releases project IDs.

### `GET /metrics`

Prometheus metrics endpoint. Key metrics:

| Metric | Description |
|---|---|
| `container_quota_used_bytes` | Current disk usage per container |
| `container_quota_limit_bytes` | Hard limit per container |
| `container_quota_usage_ratio` | Usage / limit ratio (0.0 ~ 1.0+) |
| `container_quota_total` | Count of quotas by status |

All metrics carry labels: `container_id`, `container_name`, `pod`, `pod_namespace`, `namespace`, `image`.

#### Prometheus Scrape Config

```yaml
scrape_configs:
  - job_name: quota-agent
    scrape_interval: 30s
    static_configs:
      - targets: ['<node-ip>:8080']
```

#### Example Alert Rules

```yaml
- alert: ContainerQuotaUsageHigh
  expr: container_quota_usage_ratio > 0.8
  for: 5m
  labels:
    severity: warning

- alert: ContainerQuotaUsageCritical
  expr: container_quota_usage_ratio > 0.95
  for: 1m
  labels:
    severity: critical
```

## Architecture

```
containerd-snapshot-quota/
├── cmd/main.go                     # Entry point
├── configs/                        # Example configuration files
├── docs/                           # Documentation
└── pkg/
    ├── config/                     # Config loading and validation (viper)
    ├── model/                      # Shared data types
    ├── quota/                      # Top-level agent orchestrator
    ├── runtime/
    │   └── containerd/             # Event watcher, overlay path extraction
    ├── manager/
    │   ├── core/                   # Interfaces, project ID pool, size parsing
    │   └── xfs/                    # XFS quota operations (xfs_quota / xfs_io)
    ├── metrics/                    # Prometheus collector
    ├── storage/                    # Badger DB persistence
    └── web/                        # HTTP API + /metrics endpoint
```

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `project quota not enabled` | XFS partition not mounted with `prjquota` | `mount -o remount,prjquota <path>` |
| `filesystem type "ext4" not supported` | Mount point is not XFS | Use an XFS partition for containerd data |
| Quota not applied to a container | Namespace not in config | Add the namespace to `manager.namespaces` |
| `project ID pool exhausted` | All IDs in range are allocated | Clean stale quotas or expand `projidMin`/`projidMax` |

Check real-time quota usage directly:

```bash
xfs_quota -x -c "report -h -p" /var/lib/containerd
```

## License

Apache License 2.0. See [LICENSE](LICENSE) for details.
