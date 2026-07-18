<p align="center">
  <h1 align="center">GoStreamProxy</h1>
  <p align="center">高性能 HTTPS 反向代理服务器，专为视频流转发场景设计</p>
</p>

<p align="center">
  <img src="https://img.shields.io/badge/Go-1.22+-00ADD8?style=flat-square&logo=go" alt="Go">
  <img src="https://img.shields.io/badge/License-MIT-green?style=flat-square" alt="License">
  <img src="https://img.shields.io/badge/Platform-x86%20%7C%20ARM%20%7C%20RISC--V-lightgrey?style=flat-square" alt="Platform">
  <img src="https://img.shields.io/badge/Dependencies-Zero-brightgreen?style=flat-square" alt="Dependencies">
</p>

---

## 特性一览

| 分类 | 功能 |
|------|------|
| **代理核心** | TLS HTTPS 反向代理 · 动态路由热重载 · HTTP/2 传输 · 连接池复用 |
| **流媒体** | WebSocket 代理 (ws/wss) · Range 请求透传 · 视频缓存头优化 |
| **负载均衡** | 多后端加权轮询 · 动态权重配置 · 路由热重载生效 |
| **安全** | API Key 认证 · IP 黑白名单 (CIDR) · 令牌桶限流 · 安全响应头 |
| **运维** | JSON 外部配置 · 日志轮转 · Panic 恢复 · 优雅关闭 · 健康检查 |
| **性能** | 零外部依赖 · 50K QPS · 16MB 缓冲池 · 原子操作无锁计数 |

已验证平台：x86 · ARM · RISC-V · OpenWRT (Mt7621 路由器)

---

## 快速开始

### 编译

```bash
# 克隆项目
git clone https://github.com/HaTin/GoStreamProxy.git
cd GoStreamProxy

# 编译
go build -o proxy

# 运行（默认读取当前目录 config.json）
./proxy

# 指定配置文件
./proxy /path/to/config.json
```

### 准备证书

将 TLS 证书放置到配置文件指定的路径（默认 `/etc/ca/tls.crt` 和 `/etc/ca/tls.key`）。

快速生成自签名证书（仅测试用）：

```bash
openssl req -x509 -newkey rsa:2048 -keyout key.pem -out cert.pem \
  -days 365 -nodes -subj "/CN=localhost"
```

### 验证

```bash
# 健康检查
curl -k https://localhost:8443/_health
# {"status":"ok"}
```

---

## 配置参考

所有配置通过 `config.json` 管理，未设置的字段自动使用默认值。启动时可通过命令行参数指定配置文件路径。

### 基础参数

```json
{
  "target_url": "https://www.xxx.com",
  "listen_addr": ":8443",
  "cert_file": "/etc/ca/tls.crt",
  "key_file": "/etc/ca/tls.key",
  "skip_tls_verify": true
}
```

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `target_url` | string | `https://www.xxx.com` | 后端目标服务器地址 |
| `listen_addr` | string | `:8443` | 监听地址和端口 |
| `cert_file` | string | `/etc/ca/tls.crt` | TLS 证书文件路径 |
| `key_file` | string | `/etc/ca/tls.key` | TLS 私钥文件路径 |
| `skip_tls_verify` | bool | `true` | 跳过后端 TLS 证书验证（生产环境建议关闭） |

### 路由与日志

```json
{
  "routes_file": "routes.json",
  "reload_interval_sec": 10,
  "log_file": "proxy.log",
  "log_max_size_mb": 100,
  "log_max_backups": 3,
  "log_max_age_days": 7
}
```

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `routes_file` | string | `routes.json` | 路由配置文件路径 |
| `reload_interval_sec` | int | `10` | 路由热重载间隔（秒），0=禁用 |
| `log_file` | string | `proxy.log` | 日志文件路径 |
| `log_max_size_mb` | int | `100` | 单个日志文件最大大小（MB） |
| `log_max_backups` | int | `3` | 保留的日志备份数量 |
| `log_max_age_days` | int | `7` | 日志保留天数 |

### 缓冲与连接池

```json
{
  "buffer_size_mb": 16,
  "buffer_idle_timeout_sec": 60,
  "max_idle_conns": 16,
  "max_idle_conns_per_host": 16,
  "max_conns_per_host": 16
}
```

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `buffer_size_mb` | int | `16` | 缓冲区大小（MB），影响内存占用 |
| `buffer_idle_timeout_sec` | int | `60` | 缓冲池空闲超时（秒） |
| `max_idle_conns` | int | `16` | 全局最大空闲连接数 |
| `max_idle_conns_per_host` | int | `16` | 每个后端最大空闲连接数 |
| `max_conns_per_host` | int | `16` | 每个后端最大并发连接数 |

### 超时配置

```json
{
  "read_timeout_sec": 30,
  "write_timeout_sec": 600,
  "idle_timeout_sec": 120,
  "dial_timeout_sec": 30,
  "dial_keep_alive_sec": 60,
  "idle_conn_timeout_sec": 90,
  "tls_handshake_timeout_sec": 10,
  "expect_continue_timeout_sec": 1,
  "ws_handshake_timeout_sec": 10,
  "shutdown_timeout_sec": 10,
  "memory_monitoring_interval_sec": 300
}
```

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `read_timeout_sec` | int | `30` | 读取客户端请求超时 |
| `write_timeout_sec` | int | `600` | 写入响应超时（视频流需较长） |
| `idle_timeout_sec` | int | `120` | HTTP 空闲连接超时 |
| `dial_timeout_sec` | int | `30` | TCP 拨号超时 |
| `dial_keep_alive_sec` | int | `60` | TCP KeepAlive 间隔 |
| `idle_conn_timeout_sec` | int | `90` | 空闲连接回收超时 |
| `tls_handshake_timeout_sec` | int | `10` | TLS 握手超时 |
| `expect_continue_timeout_sec` | int | `1` | Expect: 100-continue 超时 |
| `ws_handshake_timeout_sec` | int | `10` | WebSocket 握手超时 |
| `shutdown_timeout_sec` | int | `10` | 优雅关闭等待超时 |
| `memory_monitoring_interval_sec` | int | `300` | 内存监控输出间隔（秒） |

### 请求头注入

```json
{
  "fixed_headers": {
    "Host": "www.xxx.com",
    "User-Agent": "Mozilla/5.0 ...",
    "Referer": "https://www.xxx.com"
  }
}
```

固定注入到每个转发请求的 HTTP 头。留空则不注入。

---

## 路由配置

路由配置支持两种格式，可混合使用。

### 字符串格式（单后端）

```json
{
  "routes": {
    "api": "backend/api",
    "static": "assets/static",
    "media": "cdn/media"
  }
}
```

映射规则：

| 请求路径 | 转发目标 |
|----------|----------|
| `/api/test` | `{target_url}/backend/api/test` |
| `/static/js/app.js` | `{target_url}/assets/static/js/app.js` |
| `/media/video.mp4` | `{target_url}/cdn/media/video.mp4` |

### 对象格式（多后端负载均衡）

```json
{
  "routes": {
    "stream": {
      "prefix": "live",
      "backends": [
        {"url": "https://server1.example.com", "weight": 3},
        {"url": "https://server2.example.com", "weight": 1}
      ]
    }
  }
}
```

| 字段 | 说明 |
|------|------|
| `prefix` | 转发路径前缀 |
| `backends` | 后端列表 |
| `backends[].url` | 后端地址 |
| `backends[].weight` | 权重（默认 1），按比例分配请求 |

负载均衡采用加权轮询算法，权重 3:1 表示每 4 个请求中约 3 个发往 server1、1 个发往 server2。

路由配置支持热重载，修改 `routes.json` 后自动生效，无需重启服务。

---

## 安全配置

### API Key 认证

```json
{
  "auth_enabled": true,
  "api_keys": ["your-secret-key-1", "your-secret-key-2"]
}
```

客户端通过以下任一方式传递 API Key：

```bash
# 方式 1: X-API-Key 头
curl -H "X-API-Key: your-secret-key-1" https://localhost:8443/api/test

# 方式 2: Authorization Bearer
curl -H "Authorization: Bearer your-secret-key-1" https://localhost:8443/api/test
```

认证失败返回 `401 Unauthorized`。健康检查 `/_health` 端点不受认证限制。

### IP 黑白名单

```json
{
  "ip_whitelist": ["192.168.1.0/24", "10.0.0.0/8"],
  "ip_blacklist": ["203.0.113.0/24"]
}
```

- **黑名单优先**：命中黑名单直接拒绝（`403 Forbidden`）
- **白名单为空**：全部放行
- **白名单非空**：仅允许白名单中的 IP

支持 CIDR 格式（如 `192.168.1.0/24`）和单 IP（如 `192.168.1.1/32`）。

### 限流

```json
{
  "rate_limit_enabled": true,
  "rate_limit_rps": 100,
  "rate_limit_burst": 200,
  "rate_limit_cleanup_sec": 60
}
```

基于 IP 的令牌桶算法：

| 字段 | 说明 |
|------|------|
| `rate_limit_rps` | 每个 IP 每秒允许的请求数 |
| `rate_limit_burst` | 突发请求容量 |
| `rate_limit_cleanup_sec` | 过期桶清理间隔（秒） |

限流触发返回 `429 Too Many Requests`，响应头包含 `Retry-After: 1`。

### 安全响应头

以下响应头始终自动注入：

| 头 | 值 | 作用 |
|----|----|------|
| `X-Content-Type-Options` | `nosniff` | 防止 MIME 类型嗅探 |
| `Strict-Transport-Security` | `max-age=31536000; includeSubDomains` | 强制 HTTPS |
| `Access-Control-Allow-Origin` | `*` | CORS 跨域支持 |
| `Accept-Ranges` | `bytes` | 支持 Range 请求（视频拖拽） |

---

## 架构

```
客户端请求
    │
    ▼
┌──────────────────────────────┐
│       panicRecovery          │  捕获 panic → 500 + 栈日志
├──────────────────────────────┤
│       securityHeaders        │  注入 HSTS / nosniff
├──────────────────────────────┤
│       securityFilter         │
│       ├─ IP 黑白名单检查     │  拒绝 → 403
│       ├─ 令牌桶限流          │  超限 → 429
│       └─ API Key 认证        │  失败 → 401
├──────────────────────────────┤
│       proxyHandler           │
│       ├─ /_health 健康检查   │  直接返回 JSON
│       ├─ 路由匹配            │  前缀 → 后端选择
│       ├─ WebSocket 检测      │  Upgrade → hijack + 双向转发
│       └─ HTTP 反向代理       │  Director + Transport
└──────────────────────────────┘
    │
    ▼
  后端服务器
```

---

## 性能基准

测试环境：单核虚拟机，Go 1.22，wrk 压测工具

| 场景 | 并发 | QPS | P50 延迟 | P99 延迟 |
|------|------|-----|----------|----------|
| 健康检查 | 100 | 48,640 | 1.12ms | 24.83ms |
| 404 响应 | 100 | 54,188 | 1.17ms | 24.59ms |
| 代理转发 | 100 | 349 | 233ms | 1.2s |
| 代理转发 | 500 | 1,725 | 236ms | 889ms |

代理自身吞吐量约 50K QPS，瓶颈在后端延迟。500 并发下线性扩展，无崩溃。

---

## 内存估算

缓冲区内存公式：**总内存 ≈ buffer_size_mb × 并发请求数**

| 场景 | buffer_size_mb | 10 并发 | 100 并发 | 1000 并发 |
|------|---------------|---------|----------|-----------|
| 接口代理 | 4 | 40MB | 400MB | 4GB |
| 视频转发 | 16 | 160MB | 1.6GB | 16GB |
| 大文件 | 64 | 640MB | 6.4GB | - |

建议根据实际并发数和内存容量调整 `buffer_size_mb`。

---

## 部署

### 直接运行

```bash
go build -o proxy
./proxy config.json
```

### Systemd 服务

```ini
# /etc/systemd/system/gostreamproxy.service
[Unit]
Description=GoStreamProxy
After=network.target

[Service]
Type=simple
ExecStart=/usr/local/bin/proxy /etc/gostreamproxy/config.json
Restart=always
RestartSec=5
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload
sudo systemctl enable gostreamproxy
sudo systemctl start gostreamproxy
```

### Docker

```dockerfile
FROM golang:1.22-alpine AS builder
WORKDIR /app
COPY . .
RUN go build -o proxy .

FROM alpine:latest
RUN apk --no-cache add ca-certificates
COPY --from=builder /app/proxy /usr/local/bin/proxy
COPY config.json /etc/gostreamproxy/config.json
COPY routes.json /etc/gostreamproxy/routes.json
EXPOSE 8443
CMD ["proxy", "/etc/gostreamproxy/config.json"]
```

---

## 日志

日志同时输出到控制台和文件，支持自动轮转。

### 日志格式

```
2025/07/19 06:09:33 启动代理服务器
2025/07/19 06:09:33 监听: :8443 | 目标: https://www.xxx.com | 缓冲: 16MB
2025/07/19 06:09:33 功能: WebSocket代理 | 加权负载均衡 | Range缓存优化 | 安全层
2025/07/19 06:09:37 GET /api/test => /backend/api/test [backend=www.xxx.com] [::1:54816] 551ms
```

### 日志类型

| 前缀 | 含义 |
|------|------|
| `[404]` | 路由未匹配 |
| `[502]` | 后端不可达 |
| `[WS]` | WebSocket 连接事件 |
| `[PANIC]` | panic 恢复（含栈信息） |
| `[SECURITY]` | 安全事件（认证失败/限流/IP拒绝） |
| `内存:` | 定期内存监控输出 |

---

## API 端点

| 端点 | 方法 | 说明 | 认证 |
|------|------|------|------|
| `/_health` | GET | 健康检查，返回 `{"status":"ok"}` | 跳过 |

---

## 常见问题

**Q: 修改 routes.json 需要重启吗？**

不需要。路由配置支持热重载，默认每 10 秒检查一次文件变更。

**Q: 如何关闭安全认证？**

默认关闭。只需不在 config.json 中设置 `auth_enabled: true` 即可。

**Q: 视频拖拽卡顿？**

确保 `write_timeout_sec` 足够大（默认 600 秒），并根据视频大小调整 `buffer_size_mb`。

**Q: WebSocket 连接断开？**

检查 `ws_handshake_timeout_sec` 和后端是否支持 WebSocket 升级。

---

## 项目结构

```
GoStreamProxy/
├── main.go           # 主程序（全部逻辑）
├── config.json       # 配置文件
├── routes.json       # 路由映射配置
└── README.md         # 本文档
```

---

## 贡献

欢迎提交 Issue 和 Pull Request。

```bash
# Fork 并克隆
git clone https://github.com/HaTin/GoStreamProxy.git
cd GoStreamProxy

# 创建分支
git checkout -b feature/your-feature

# 提交
git commit -m "feat: add your feature"
git push origin feature/your-feature
```

---

## License

[MIT License](LICENSE) - Copyright (c) 2025 Huatin-Nice
