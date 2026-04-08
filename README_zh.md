# Containerd Snapshot Quota

[English](README.md) | [中文](README_zh.md)

[![Go Version](https://img.shields.io/github/go-mod/go-version/0x0034/containerd-snapshot-quota)](go.mod)
[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)

一个轻量级守护进程，通过 XFS 项目配额（Project Quota）为每个容器的可写层实施磁盘限额，与 containerd 运行时集成。它透明地限制每个容器可以使用的磁盘空间 — 无需修改容器镜像、运行时配置，也不影响容器的正常运行。

## 工作原理

```
                        containerd
                            │
               TaskCreate / TaskDelete 事件
                            │
                    ┌───────▼────────┐
                    │  Quota Agent   │
                    │                │
                    │  1. 从 /proc/<pid>/mountinfo 提取 overlay upperdir/workdir
                    │  2. 分配 XFS Project ID
                    │  3. 绑定 Project ID 到目录 (xfs_quota project -s)
                    │  4. 设置磁盘限额 (xfs_quota limit -p)
                    │  5. 持久化状态到 Badger DB
                    └───────┬────────┘
                            │
                XFS 内核层面强制执行配额
                            │
                  容器达到限额时收到 ENOSPC
```

**核心特性：**

- 配额在内核层面执行 — 运行时零开销
- 容器无感知；达到限额时收到 `ENOSPC`，与磁盘写满行为一致
- 适用于任何使用 overlayfs 的 OCI 运行时（Docker、nerdctl、Kubernetes CRI）
- Agent 重启后自动恢复 — 状态持久化存储

## 功能特性

- **事件驱动** — 订阅 containerd `TaskCreate` / `TaskDelete` 事件，无轮询
- **规则匹配** — 按容器名、镜像、Kubernetes Pod 元数据设置不同的配额大小
- **自动配额** — 可选为所有容器自动应用默认配额，无需配置规则
- **Prometheus 监控** — 通过 `/metrics` 端点实时暴露每个容器的磁盘使用量、限额和使用率
- **HTTP API** — 通过 REST 接口查询和清理配额记录
- **状态恢复** — 重启后自动重新应用；过期记录自动回收
- **多命名空间** — 监听 `default`、`moby`、`k8s.io` 或任何自定义 containerd 命名空间

## 环境要求

| 要求 | 详情 |
|---|---|
| 操作系统 | Linux |
| 文件系统 | XFS 且以 `prjquota` 选项挂载 |
| 运行时 | containerd 1.6+ |
| 系统工具 | `xfs_quota`、`xfs_io`（由 `xfsprogs` 提供） |

### 启用 XFS 项目配额

```bash
# 立即重新挂载（非持久化）
mount -o remount,prjquota /var/lib/containerd

# 持久化 — 在 /etc/fstab 中添加 prjquota
# /dev/sda1  /var/lib/containerd  xfs  defaults,prjquota  0 0
```

验证：

```bash
xfs_quota -x -c "state" /var/lib/containerd
# 应显示：Accounting: ON, Enforcement: ON
```

## 快速开始

### 编译

```bash
git clone https://github.com/0x0034/containerd-snapshot-quota.git
cd containerd-snapshot-quota
go build -o quota-agent ./cmd/main.go
```

### 配置

```bash
mkdir -p /etc/quota-agent
cp configs/config.yaml /etc/quota-agent/config.yaml
```

编辑 `/etc/quota-agent/config.yaml`：

```yaml
version: v1
persistDir: /var/lib/quota-agent

manager:
  socket: /run/containerd/containerd.sock
  mountPoint: /var/lib/containerd    # 必须在 XFS 分区上
  size: "10G"                        # 每个容器的默认配额
  autoQuota: true
  namespaces:
    - k8s.io

web:
  enabled: true
  port: 8080
```

### 运行

```bash
./quota-agent -config /etc/quota-agent/config.yaml
```

### 部署为 systemd 服务

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

## 配置说明

### 运行模式

| 模式 | `autoQuota` | `matchRules` | 行为 |
|---|---|---|---|
| **OnlyAuto** | `true` | 空 | 所有容器使用默认配额 |
| **OnlyRules** | `false` | 有规则 | 仅匹配规则的容器设置配额 |
| **All** | `true` | 有规则 | 匹配的容器使用规则配额；其余使用默认配额 |

### 匹配规则

规则按定义顺序匹配，第一个命中的规则生效。

```yaml
manager:
  matchRules:
    # 按镜像名匹配
    - name: databases
      size: "100G"
      level: image
      selector:
        name: mysql

    # 按 Kubernetes Pod 匹配
    - name: prod-api
      size: "50G"
      level: pod
      selector:
        name: api-server
        namespace: production

    # 按容器名匹配（子串匹配）
    - name: log-collectors
      size: "30G"
      level: container
      selector:
        name: filebeat
```

### 环境变量覆盖

所有配置项都可以通过 `QUOTA_` 前缀的环境变量覆盖：

```bash
export QUOTA_MANAGER_SIZE=50G
export QUOTA_MANAGER_MOUNTPOINT=/data/containerd
export QUOTA_WEB_ENABLED=false
```

## API 接口

### `GET /health`

```json
{"status": "ok"}
```

### `GET /api/quotas?page=1&pageSize=20`

返回分页的配额记录，包含容器元数据和状态。

### `POST /api/quotas/clean`

清除所有配额记录并释放 Project ID。

### `GET /metrics`

Prometheus 指标端点。主要指标：

| 指标 | 说明 |
|---|---|
| `container_quota_used_bytes` | 每个容器当前磁盘使用量 |
| `container_quota_limit_bytes` | 每个容器的硬限制 |
| `container_quota_usage_ratio` | 使用率（已用 / 限制，0.0 ~ 1.0+） |
| `container_quota_total` | 各状态配额总数 |

所有指标包含标签：`container_id`、`container_name`、`pod`、`pod_namespace`、`namespace`、`image`。

#### Prometheus 采集配置

```yaml
scrape_configs:
  - job_name: quota-agent
    scrape_interval: 30s
    static_configs:
      - targets: ['<节点IP>:8080']
```

#### 告警规则示例

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

## 项目架构

```
containerd-snapshot-quota/
├── cmd/main.go                     # 程序入口
├── configs/                        # 示例配置文件
├── docs/                           # 文档
└── pkg/
    ├── config/                     # 配置加载与校验（viper）
    ├── model/                      # 共享数据类型
    ├── quota/                      # 顶层 Agent 编排器
    ├── runtime/
    │   └── containerd/             # 事件监听、overlay 路径提取
    ├── manager/
    │   ├── core/                   # 接口、Project ID 池、大小解析
    │   └── xfs/                    # XFS 配额操作（xfs_quota / xfs_io）
    ├── metrics/                    # Prometheus 指标采集器
    ├── storage/                    # Badger DB 持久化
    └── web/                        # HTTP API + /metrics 端点
```

## 故障排查

| 现象 | 原因 | 解决方案 |
|---|---|---|
| `project quota not enabled` | XFS 分区未以 `prjquota` 选项挂载 | `mount -o remount,prjquota <路径>` |
| `filesystem type "ext4" not supported` | 挂载点不是 XFS 文件系统 | 使用 XFS 分区存放 containerd 数据 |
| 容器未被设置配额 | 命名空间不在配置中 | 将命名空间添加到 `manager.namespaces` |
| `project ID pool exhausted` | 范围内所有 ID 已分配 | 清理过期配额或扩大 `projidMin`/`projidMax` |

直接查看实时配额使用情况：

```bash
xfs_quota -x -c "report -h -p" /var/lib/containerd
```

## 许可证

Apache License 2.0。详见 [LICENSE](LICENSE)。
