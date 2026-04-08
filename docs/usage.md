# Containerd Snapshot Quota 使用文档

## 概述

Containerd Snapshot Quota 是一个基于 XFS 项目配额（Project Quota）的容器磁盘限额工具。它通过监听 containerd 容器生命周期事件，自动为容器的 overlay 文件系统（upperdir/workdir）设置磁盘配额，从而限制单个容器的磁盘使用量，且不影响容器的正常运行。

### 工作原理

1. 监听 containerd 的 `TaskCreate` 和 `TaskDelete` 事件
2. 容器启动时，从 `/proc/<pid>/mountinfo` 提取 overlay 的 `upperdir` 和 `workdir` 路径
3. 分配一个唯一的 XFS Project ID，并将其绑定到目标目录
4. 通过 `xfs_quota` 命令设置磁盘配额（soft/hard limit）
5. 容器销毁时，自动清理配额和 Project ID

---

## 前置条件

### 系统要求

- Linux 操作系统
- XFS 文件系统（containerd 数据目录所在分区）
- containerd 运行时

### XFS 项目配额启用

containerd 数据目录所在的 XFS 分区必须以 `prjquota` 选项挂载：

```bash
# 查看当前挂载选项
mount | grep xfs

# 如果未启用 prjquota，需要重新挂载
mount -o remount,prjquota /var/lib/containerd

# 或在 /etc/fstab 中添加 prjquota 选项（永久生效）
# /dev/sda1  /var/lib/containerd  xfs  defaults,prjquota  0 0
```

验证配额状态：

```bash
xfs_quota -x -c "state" /var/lib/containerd
```

输出应包含：
```
Project quota state on /var/lib/containerd (/dev/sda1)
  Accounting: ON
  Enforcement: ON
```

### 系统工具依赖

确保以下命令可用：

| 命令 | 用途 | 安装方式 |
|------|------|----------|
| `xfs_quota` | 配额管理 | `yum install xfsprogs` 或 `apt install xfsprogs` |
| `xfs_io` | 读取 inode Project ID | 同上 |

---

## 安装

### 编译

```bash
git clone https://github.com/0x0034/containerd-snapshot-quota.git
cd containerd-snapshot-quota

go build -o quota-agent ./cmd/main.go
```

### 部署

```bash
# 复制二进制
cp quota-agent /usr/local/bin/

# 创建配置目录
mkdir -p /etc/quota-agent

# 复制配置文件
cp configs/config.yaml /etc/quota-agent/config.yaml

# 创建数据目录
mkdir -p /var/lib/quota-agent
```

---

## 配置说明

配置文件路径默认为 `/etc/quota-agent/config.yaml`，可通过 `-config` 参数指定。

### 完整配置示例

```yaml
version: v1
persistDir: /var/lib/quota-agent    # 状态持久化目录（Badger DB）
logLevel: info                       # 日志级别

manager:
  runtime: containerd
  socket: /run/containerd/containerd.sock  # containerd socket 路径
  mountPoint: /var/lib/containerd          # containerd 数据目录（XFS 分区）
  size: "10G"                              # 默认配额大小
  projidMin: 1000                          # Project ID 范围起始
  projidMax: 65534                         # Project ID 范围结束
  autoQuota: true                          # 是否自动为所有容器设置配额
  namespaces:                              # 监听的 containerd 命名空间
    - default
    - moby
    - k8s.io

  matchRules: []                           # 匹配规则（详见下文）

web:
  host: 0.0.0.0
  port: 8080                               # HTTP API 监听端口
```

### 配置项详解

#### `manager.mountPoint`

containerd 存储数据所在的目录路径。该目录必须位于启用了 `prjquota` 的 XFS 分区上。

#### `manager.size`

默认配额大小。支持的单位：
- `K` / `KB` — 千字节
- `M` / `MB` — 兆字节
- `G` / `GB` — 吉字节
- `T` / `TB` — 太字节

示例：`"10G"`、`"500M"`、`"1T"`

#### `manager.projidMin` / `manager.projidMax`

XFS Project ID 的可用范围。每个容器分配一个唯一的 Project ID。确保该范围不与系统中其他用途的 Project ID 冲突。

#### `manager.autoQuota`

- `true` — 对所有容器自动应用默认配额（`manager.size`）
- `false` — 仅对匹配规则的容器应用配额

#### `manager.namespaces`

containerd 命名空间列表。常见值：

| 命名空间 | 说明 |
|----------|------|
| `default` | containerd 默认命名空间 |
| `moby` | Docker 使用的命名空间 |
| `k8s.io` | Kubernetes 使用的命名空间 |

---

## 运行模式

根据 `autoQuota` 和 `matchRules` 的配置组合，系统有四种运行模式：

| 模式 | autoQuota | matchRules | 行为 |
|------|-----------|------------|------|
| **Off** | `false` | 空 | 不应用任何配额（启动时报错） |
| **OnlyAuto** | `true` | 空 | 所有容器使用默认配额 |
| **OnlyRules** | `false` | 有规则 | 仅匹配规则的容器设置配额，其余跳过 |
| **All** | `true` | 有规则 | 匹配规则的容器使用规则配额，其余使用默认配额 |

---

## 匹配规则

匹配规则用于为不同的容器指定不同的配额大小。规则按定义顺序匹配，**第一个匹配的规则生效**。

### 规则结构

```yaml
matchRules:
  - name: <规则名称>       # 标识用途，便于管理
    size: "<配额大小>"      # 该规则的配额大小
    level: <匹配级别>       # container / image / pod
    selector:               # 匹配条件
      <key>: <value>
```

### 匹配级别

#### 1. `container` — 按容器名称匹配

```yaml
matchRules:
  - name: nginx-containers
    size: "20G"
    level: container
    selector:
      name: nginx           # 容器名称包含 "nginx" 即匹配
```

#### 2. `image` — 按镜像匹配

```yaml
matchRules:
  - name: large-images
    size: "50G"
    level: image
    selector:
      name: mysql            # 镜像名称包含 "mysql"
      tag: "8.0"             # 镜像 tag 包含 "8.0"（可选）
```

#### 3. `pod` — 按 Pod 匹配（Kubernetes 环境）

```yaml
matchRules:
  - name: production-pods
    size: "100G"
    level: pod
    selector:
      name: api-server       # Pod 名称包含 "api-server"
      namespace: production  # Pod 命名空间精确匹配 "production"（可选）
```

### 典型规则配置

```yaml
matchRules:
  # 数据库类容器给大配额
  - name: database
    size: "100G"
    level: image
    selector:
      name: mysql

  - name: postgres
    size: "100G"
    level: image
    selector:
      name: postgres

  # 日志采集容器给中等配额
  - name: log-collector
    size: "30G"
    level: container
    selector:
      name: filebeat

  # 生产环境关键服务给大配额
  - name: prod-critical
    size: "50G"
    level: pod
    selector:
      name: order-service
      namespace: production
```

---

## 启动与运行

### 命令行参数

```bash
quota-agent [flags]

Flags:
  -config string    配置文件路径（默认 /etc/quota-agent/config.yaml）
  -v int            日志详细级别（0-4，数字越大越详细）
  -logtostderr      日志输出到 stderr（默认 true）
```

### 直接运行

```bash
quota-agent -config /etc/quota-agent/config.yaml -v 2
```

### systemd 服务

创建 `/etc/systemd/system/quota-agent.service`：

```ini
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
```

启动服务：

```bash
systemctl daemon-reload
systemctl enable quota-agent
systemctl start quota-agent
systemctl status quota-agent
```

---

## 环境变量覆盖

所有配置项都可以通过环境变量覆盖，前缀为 `QUOTA_`，层级用 `_` 分隔：

```bash
# 覆盖默认配额大小
export QUOTA_MANAGER_SIZE=50G

# 覆盖 containerd socket 路径
export QUOTA_MANAGER_SOCKET=/run/k3s/containerd/containerd.sock

# 覆盖挂载点
export QUOTA_MANAGER_MOUNTPOINT=/data/containerd

# 覆盖 Web 端口
export QUOTA_WEB_PORT=9090
```

---

## HTTP API

### 健康检查

```bash
curl http://localhost:8080/health
```

响应：
```json
{"status": "ok"}
```

### 查询配额列表

```bash
# 默认分页
curl http://localhost:8080/api/quotas

# 指定分页
curl "http://localhost:8080/api/quotas?page=1&pageSize=50"
```

响应：
```json
{
  "page": 1,
  "pageSize": 20,
  "total": 5,
  "totalPages": 1,
  "quotas": [
    {
      "ID": "a1b2c3d4e5f6...",
      "ProjectID": 1001,
      "Status": "succeed",
      "Message": "",
      "Size": "10G",
      "CInfo": {
        "id": "a1b2c3d4e5f6...",
        "name": "nginx",
        "namespace": "k8s.io",
        "upperdir": "/var/lib/containerd/io.containerd.snapshotter.v1.overlayfs/snapshots/123/fs",
        "workdir": "/var/lib/containerd/io.containerd.snapshotter.v1.overlayfs/snapshots/123/work",
        "image": "docker.io/library/nginx:latest",
        "podName": "web-server-7d4f8b6c9-x2k4m",
        "podNamespace": "default"
      },
      "CreatedAt": "2026-04-08T15:30:00Z"
    }
  ]
}
```

配额状态说明：

| Status | 说明 |
|--------|------|
| `pending` | 配额正在设置中 |
| `succeed` | 配额设置成功 |
| `failed` | 配额设置失败，查看 `Message` 字段获取原因 |

### 清理所有配额

```bash
curl -X POST http://localhost:8080/api/quotas/clean
```

响应：
```json
{"cleaned": 5, "total": 5}
```

> **注意**：此操作会清除所有已设置的配额记录和 Project ID 绑定。

---

## Prometheus 监控指标

Quota Agent 内置 Prometheus metrics 端点，可以实时监控每个容器/Pod 的磁盘配额和使用情况。

### 访问方式

```bash
curl http://localhost:8080/metrics
```

### 指标列表

| 指标名 | 类型 | 说明 | 标签 |
|--------|------|------|------|
| `container_quota_limit_bytes` | Gauge | 配额硬限制（字节） | container_id, container_name, pod, pod_namespace, namespace, image, status |
| `container_quota_soft_limit_bytes` | Gauge | 配额软限制（字节） | container_id, container_name, pod, pod_namespace, namespace, image |
| `container_quota_used_bytes` | Gauge | 实际磁盘使用量（字节） | container_id, container_name, pod, pod_namespace, namespace, image |
| `container_quota_usage_ratio` | Gauge | 使用率（已用/硬限制，0.0~1.0+） | container_id, container_name, pod, pod_namespace, namespace, image |
| `container_quota_total` | Gauge | 各状态配额总数 | status |

### 标签说明

| 标签 | 说明 | 示例 |
|------|------|------|
| `container_id` | 容器 ID（前 12 位） | `a1b2c3d4e5f6` |
| `container_name` | 容器名称 | `nginx` |
| `pod` | Pod 名称（K8s 环境） | `web-server-7d4f8b6c9-x2k4m` |
| `pod_namespace` | Pod 命名空间（K8s 环境） | `default` |
| `namespace` | containerd 命名空间 | `k8s.io` |
| `image` | 容器镜像 | `docker.io/library/nginx:latest` |
| `status` | 配额状态 | `succeed` / `pending` / `failed` |

### 输出示例

```
# HELP container_quota_limit_bytes Configured quota hard limit in bytes for a container
# TYPE container_quota_limit_bytes gauge
container_quota_limit_bytes{container_id="a1b2c3d4e5f6",container_name="nginx",image="docker.io/library/nginx:latest",namespace="k8s.io",pod="web-server-7d4f8b6c9-x2k4m",pod_namespace="default",status="succeed"} 1.073741824e+10

# HELP container_quota_used_bytes Actual disk usage in bytes for a container under quota
# TYPE container_quota_used_bytes gauge
container_quota_used_bytes{container_id="a1b2c3d4e5f6",container_name="nginx",image="docker.io/library/nginx:latest",namespace="k8s.io",pod="web-server-7d4f8b6c9-x2k4m",pod_namespace="default"} 2.684354560e+09

# HELP container_quota_usage_ratio Ratio of used bytes to hard limit (0.0 - 1.0+)
# TYPE container_quota_usage_ratio gauge
container_quota_usage_ratio{container_id="a1b2c3d4e5f6",container_name="nginx",image="docker.io/library/nginx:latest",namespace="k8s.io",pod="web-server-7d4f8b6c9-x2k4m",pod_namespace="default"} 0.25

# HELP container_quota_total Total number of container quotas by status
# TYPE container_quota_total gauge
container_quota_total{status="succeed"} 5
container_quota_total{status="pending"} 0
container_quota_total{status="failed"} 1
```

### Prometheus 采集配置

在 `prometheus.yml` 中添加采集目标：

```yaml
scrape_configs:
  - job_name: 'quota-agent'
    scrape_interval: 30s
    static_configs:
      - targets: ['<node-ip>:8080']
        labels:
          node: '<node-name>'
```

如果通过 Kubernetes ServiceMonitor 采集：

```yaml
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: quota-agent
  namespace: monitoring
spec:
  selector:
    matchLabels:
      app: quota-agent
  endpoints:
    - port: http
      path: /metrics
      interval: 30s
```

### 常用告警规则

```yaml
groups:
  - name: quota-agent
    rules:
      # 容器磁盘使用率超过 80%
      - alert: ContainerQuotaUsageHigh
        expr: container_quota_usage_ratio > 0.8
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "容器 {{ $labels.pod }}/{{ $labels.container_name }} 磁盘使用率超过 80%"
          description: "当前使用率 {{ $value | humanizePercentage }}，限额 {{ with printf `container_quota_limit_bytes{container_id=\"%s\"}` $labels.container_id | query }}{{ . | first | value | humanize1024 }}B{{ end }}"

      # 容器磁盘使用率超过 95%（即将写满）
      - alert: ContainerQuotaUsageCritical
        expr: container_quota_usage_ratio > 0.95
        for: 1m
        labels:
          severity: critical
        annotations:
          summary: "容器 {{ $labels.pod }}/{{ $labels.container_name }} 磁盘即将写满"
          description: "当前使用率 {{ $value | humanizePercentage }}，请立即处理"

      # 存在失败的配额设置
      - alert: ContainerQuotaSetupFailed
        expr: container_quota_total{status="failed"} > 0
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "有 {{ $value }} 个容器配额设置失败"
```

### Grafana 面板建议

推荐创建以下面板：

1. **配额使用率 Top 10**：`topk(10, container_quota_usage_ratio)`
2. **磁盘使用量趋势**：`container_quota_used_bytes` 按 pod 分组
3. **配额状态分布**：`container_quota_total` 饼图
4. **即将超限的容器**：`container_quota_usage_ratio > 0.7` 表格，显示 pod、namespace、使用量、限额

---

## 状态恢复

Quota Agent 使用 Badger 嵌入式数据库持久化配额状态。当 Agent 重启时：

1. 从数据库中加载所有配额记录
2. 检查 `upperdir` 路径是否仍然存在
   - 路径不存在 → 清理该记录（容器已被删除）
   - 路径存在 → 标记 Project ID 为已使用
3. 对状态不是 `succeed` 的记录重新应用配额
4. 同步当前所有运行中的容器

这保证了 Agent 重启后配额不会丢失，且不会出现 Project ID 冲突。

---

## 运维排查

### 查看容器当前配额

```bash
# 查看所有 Project 配额使用情况
xfs_quota -x -c "report -h -p" /var/lib/containerd
```

输出示例：
```
Project quota on /var/lib/containerd (/dev/sda1)
                        Used   Soft   Hard
#1001                  2.5G    10G    10G
#1002                  156M    10G    10G
#1003                  8.2G    50G    50G
```

### 查看目录绑定的 Project ID

```bash
xfs_io -r -c "stat" /var/lib/containerd/io.containerd.snapshotter.v1.overlayfs/snapshots/123/fs
```

### 查看配额状态是否启用

```bash
xfs_quota -x -c "state" /var/lib/containerd
```

### 手动清除某个 Project ID 的配额

```bash
# 清除限额
xfs_quota -x -c "limit -p bsoft=0 bhard=0 1001" /var/lib/containerd
```

### 常见问题

#### Q: 启动报错 "project quota not enabled"

**原因**：XFS 分区未以 `prjquota` 选项挂载。

**解决**：
```bash
mount -o remount,prjquota /var/lib/containerd
```
并将 `prjquota` 添加到 `/etc/fstab` 以持久化。

#### Q: 启动报错 "filesystem type xxx not supported"

**原因**：`mountPoint` 对应的分区不是 XFS 文件系统。

**解决**：确认 containerd 数据目录所在分区为 XFS。可用 `df -T /var/lib/containerd` 检查。

#### Q: 容器正常运行但配额未生效

排查步骤：
1. 检查 Agent 日志：`journalctl -u quota-agent -f`
2. 确认容器所在命名空间在配置的 `namespaces` 列表中
3. 使用 API 查看配额状态：`curl http://localhost:8080/api/quotas`
4. 手动检查 Project ID：`xfs_io -r -c "stat" <upperdir路径>`

#### Q: Project ID 耗尽

**原因**：`projidMin` 到 `projidMax` 范围内的 ID 全部被占用。

**解决**：
1. 清理不再使用的配额：`curl -X POST http://localhost:8080/api/quotas/clean`
2. 扩大 Project ID 范围（修改配置后重启）

#### Q: 对容器运行有什么影响？

**无影响**。XFS Project Quota 在内核层面透明地限制目录的磁盘使用量。容器内部看不到配额的存在，只会在写入超出限额时收到磁盘空间不足的错误（`ENOSPC`），行为与磁盘写满一致。
