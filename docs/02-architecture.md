# 系统架构与请求生命周期

## 总体结构

```text
┌──────────────────────────────────────────────────────────┐
│ Custom GPT / ChatGPT Action                              │
│  - 导入 /openapi.json                                    │
│  - Bearer token                                           │
└──────────────────────────────┬───────────────────────────┘
                               │ HTTPS :443
                               ▼
┌──────────────────────────────────────────────────────────┐
│ Caddy                                                     │
│  - 公共 CA TLS                                            │
│  - zstd/gzip                                               │
│  - response_header_timeout 40s                            │
│  - reverse_proxy 127.0.0.1:8787                          │
└──────────────────────────────┬───────────────────────────┘
                               │ localhost HTTP
                               ▼
┌──────────────────────────────────────────────────────────┐
│ mygpt-caddy Go 服务                                       │
│                                                          │
│  HTTP adapter                                             │
│   ├─ Bearer 鉴权 / GPT ID 白名单                         │
│   ├─ JSON 校验与 99k 请求上限                            │
│   └─ /openapi.json /health /v1/command/run                │
│                                                          │
│  Session store                                            │
│   ├─ conversation + ephemeral user -> 哈希 key           │
│   ├─ 当前工作目录                                        │
│   └─ 原子写入 sessions.json                               │
│                                                          │
│  File pipeline                                            │
│   ├─ 下载 OpenAI 临时文件                                 │
│   ├─ 限制 host/size/timeout                                │
│   └─ HMAC 签名输出附件                                    │
│                                                          │
│  Executor                                                 │
│   ├─ /bin/bash --noprofile --norc                         │
│   ├─ 非交互环境                                            │
│   ├─ Setpgid=true                                          │
│   └─ 38s context + kill(-pid, SIGKILL)                    │
└──────────────────────────────┬───────────────────────────┘
                               │
                               ▼
                      VPS 文件系统 / Shell
```

## 一次命令的生命周期

```text
请求到达
   │
   ├─ Authorization 校验
   ├─ Content-Type 和 JSON 校验
   ├─ 读取会话 Header
   ├─ 获取会话当前目录
   ├─ 下载 openaiFileIdRefs（如有）
   ├─ 创建最长 38s 的 context
   │
   ├─ 启动 Bash 进程组
   │    ├─ stdout/stderr 写入受限文件
   │    ├─ 注入 OPENAI_FILE_* 环境变量
   │    └─ EXIT trap 记录最终 pwd
   │
   ├─ 正常结束 / 超时杀组
   ├─ 持久化最终 workdir
   ├─ 输出 ≤ 30k：内联 JSON
   ├─ 输出 > 30k：注册签名文件 URL
   └─ 删除本次输入文件临时目录
```

## HTTP 路由

| 路由 | 鉴权 | 用途 |
| --- | --- | --- |
| `GET /health` | 否 | Caddy、systemd、外部探活 |
| `GET /openapi.json` | 否 | Custom GPT Builder 导入 Schema |
| `POST /v1/command/run` | Bearer | 执行一次命令或脚本 |
| `GET /v1/files/download/{id}/{name}` | HMAC URL | ChatGPT 拉取大输出文本附件 |

文件下载路由不要求 Bearer，因为 OpenAI 拉取 `openaiFileResponse` URL 时使用的是 URL 本身。URL 包含过期时间和 HMAC 签名，服务只接受进程内登记过的附件。

## 会话状态

会话 key 为 `SHA-256(ephemeral_user_id + NUL + conversation_id)` 的前 16 字节十六进制值，不把原始 Header 写入 `sessions.json`。

- 有 `Openai-Conversation-Id` 或临时用户 ID：命令结束后的目录可供下一次调用使用。
- 两个 Header 都缺失：使用默认 `WORKSPACE_ROOT`，不建立可恢复的会话 key。
- 同一会话的命令串行执行，避免并发调用互相覆盖 cwd。
- 会话状态原子写入，服务重启后可恢复最后目录。

## 命令环境

服务追加以下非交互变量：

```text
DEBIAN_FRONTEND=noninteractive
PAGER=cat
GIT_PAGER=cat
SYSTEMD_PAGER=cat
GIT_TERMINAL_PROMPT=0
GCM_INTERACTIVE=never
CI=1
```

命令还可以读取：

```text
OPENAI_FILE_DIR
OPENAI_FILE_PATHS_JSON
OPENAI_CONVERSATION_ID
OPENAI_EPHEMERAL_USER_ID
OPENAI_GPT_ID
```

这些变量面向 VPS 上的命令脚本，不是对 ChatGPT 的额外协议承诺。

