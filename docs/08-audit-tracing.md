# 全链路追溯与审计

## 目标与边界

追溯系统回答五个问题：请求有没有到 VPS、在哪一层失败、执行了什么命令、文件从哪里下载且耗时多久、返回结果如何。

OpenAI 平台内部发生但尚未发到公网的尝试，VPS 无法直接观测。此时以“对应时段无 Caddy 入口记录”作为明确的边界证据，而不是伪造一个服务端事件。`ClientResponseError` 且 Caddy 无记录，应检查 GPT Builder 保存的 Schema、域名和认证配置。

## 两层链路

```text
OpenAI Action
    │  HTTPS 请求；OpenAI 内部错误在本机不可见
    ▼
Caddy :443
    │  /var/log/caddy/mygpt-caddy-access.json
    │  TLS、来源 IP、方法、路径、状态、总耗时、trace_id
    │  Authorization 默认脱敏
    ▼  X-Request-Id: trace_id
Go HTTP adapter
    │
    ├─ request.received / request.completed
    ├─ authentication / gpt_authorization
    ├─ request.validation / public_url / session.lock
    ├─ upload.validation / upload.download / upload.cleanup
    ├─ execution.prepare / execution.start / execution.interrupt
    ├─ execution.complete / session.persist
    ├─ output.capture / output.route / output.artifact
    └─ artifact.download
        │
        ▼
/var/lib/mygpt-caddy/audit/audit-YYYY-MM-DD.jsonl
        │
        ▼
mygpt-audit recent | trace TRACE_ID | verify
```

Caddy 生成每请求 UUID，同时写入入口日志并通过 `X-Request-Id` 传给只监听 loopback 的 Go 服务。Go 仅信任来自 loopback 的该 Header；其他请求会重新生成随机 ID。`/health` 被排除以避免探活噪声；带 HMAC 查询参数的输出附件路由由 Go 审计、从 Caddy access log 排除，避免把短期签名写入入口日志。

## 事件格式

每行是独立 JSON：

```json
{
  "schema": "mygpt.audit.v1",
  "timestamp": "2026-08-23T19:20:00.123456789Z",
  "chain_id": "...",
  "trace_id": "...",
  "sequence": 8,
  "stage": "execution.complete",
  "outcome": "failed",
  "data": {
    "exit_code": 7,
    "duration_ms": 31,
    "workdir": "/root/workspace",
    "stdout_bytes": 0,
    "stderr_bytes": 12,
    "stderr_sha256": "...",
    "stderr_tail": "test failed"
  },
  "previous_hash": "...",
  "hash": "..."
}
```

- `trace_id`：连接 Caddy 和应用事件。
- `sequence`：同一请求内严格递增；并发文件下载也不会产生乱序序号。
- `stage`：稳定的处理阶段；新增 Sink 或存储后端不改变业务代码。
- `outcome`：`started`、`succeeded`、`failed`、`timed_out`、`skipped` 等。
- `previous_hash` / `hash`：同一进程链的 SHA-256 前向链接。

服务重启会开启新的 `chain_id`。哈希链可发现保留范围内的内容修改、事件重排和中间删除；单独删除整条链的前缀/尾部需要外部检查点才能发现。它不是外部时间戳或不可抵赖签名，拥有 root 且能重写整条链的攻击者仍可重新计算哈希。需要更强保证时，可通过 `audit.Sink` 将事件同步到远端只追加存储或 SIEM。

## 记录内容

### 连接与协议

- 公网入口来源、TLS、方法、路径、状态和总耗时由 Caddy 记录。
- Go 记录请求到达、鉴权结果、GPT ID 白名单结果、JSON 校验和响应结果。
- 会话 ID 和临时用户 ID 只存 SHA-256，不写明文。

### 文件输入

每个 `openaiFileIdRefs` 文件记录：

- 安全化后的文件名、file ID、MIME、源 host；
- 下载开始、HTTP 状态、最终 host、重定向次数；
- 字节数、耗时和具体错误；
- 批次结果与临时目录清理结果。

不会记录带签名的 `download_link`。默认只允许 HTTPS 的 `*.oaiusercontent.com`，每文件 10 秒、10 MB。

### 命令与输出

- 记录命令明文、字节数和 SHA-256，审计目录仅 root 可读。
- stdin 不存明文，只存字节数和 SHA-256。
- 记录工作目录、PID/进程组、超时、SIGKILL、退出码和耗时。
- stdout/stderr 记录字节数、SHA-256 和可配置的尾部文本。
- 大输出记录附件 ID、文件名、大小、到期时间及后续下载结果；不记录签名 URL。

Shell 退出码非零的 `execution.complete` 标记为 `failed`，但 HTTP 仍返回 200，确保 GPT 能读取 stderr 并修复。协议失败才返回 400，鉴权失败返回 401。

## 配置与权限

```env
AUDIT_ENABLED=true
AUDIT_DIR=/var/lib/mygpt-caddy/audit
AUDIT_RETENTION_DAYS=30
AUDIT_FSYNC=true
AUDIT_OUTPUT_CHARS=4000
```

- 目录强制 `0700`，JSONL 强制 `0600`，systemd 使用 `UMask=0077`。
- `AUDIT_FSYNC=true` 每事件落盘，优先可靠性；高负载场景可关闭以减少同步写延迟。
- `AUDIT_OUTPUT_CHARS=0` 禁用输出明文，仅保留字节数和哈希。
- 按 UTC 日期轮转，默认保留 30 天。
- Caddy 入口日志为 `0600`，20 MiB 或每日轮转，最多 30 个文件/30 天。

## 查询与校验

```bash
# 最近 50 个事件
sudo mygpt-audit recent

# 最近 200 个事件
sudo mygpt-audit -limit 200 recent

# 一个请求的完整时间线
sudo mygpt-audit trace TRACE_ID

# 校验所有保留文件中的哈希链
sudo mygpt-audit verify

# 查看公网入口
sudo jq -c '{ts,status,duration,trace_id,request}' \
  /var/log/caddy/mygpt-caddy-access.json
```

`X-Request-Id` 会返回给调用方；出现错误时保留它即可精确查询。

## 故障定位矩阵

| Caddy 入口 | Go 审计 | 结论 |
| --- | --- | --- |
| 无 | 无 | OpenAI 尚未把请求发到 VPS；检查 Builder Schema、域名、认证或 OpenAI 平台状态 |
| 有，502/504 | 无或只有开始 | Caddy 到 `127.0.0.1:8787` 的反代、服务状态或上游超时问题 |
| 有 | `authentication failed` | Bearer token 缺失或错误 |
| 有 | `request.validation failed` | Content-Type、JSON、字段或命令参数问题 |
| 有 | `upload.download failed` | 临时链接、域名白名单、超时、HTTP 状态或大小限制问题 |
| 有 | `execution.start failed` | Bash/工作目录/进程启动问题 |
| 有 | `execution.interrupt timed_out` | 命令达到 38 秒以内的请求上限，进程组已终止 |
| 有 | `execution.complete failed` | Shell 命令已运行但退出码非零；查看 stderr tail/hash |
| 有 | `request.completed succeeded` | 服务已给 Caddy 写完正常响应；客户端仍报错时检查 OpenAI 对响应的解析或平台状态 |

## 扩展点

业务层只依赖 `audit.Sink`：

```go
type Sink interface {
    Append(Event) error
    Close() error
}
```

当前实现是本地 JSONL。后续可增加 fan-out、journald、Loki、OpenTelemetry、数据库或远端 WORM Sink，不需要把网络/存储逻辑耦合进鉴权、下载器和执行器。

## 参考

- [OpenAI：Production notes on GPT Actions](https://developers.openai.com/api/docs/actions/production)
- [OpenAI：Sending and returning files with GPT Actions](https://developers.openai.com/api/docs/actions/sending-files)
- [Caddy：`log` 指令与敏感 Header 脱敏](https://caddyserver.com/docs/caddyfile/directives/log)
- [Caddy：`log_skip` 指令](https://caddyserver.com/docs/caddyfile/directives/log_skip)
