# 故障排查

## `ClientResponseError` 且 VPS 没有请求日志

优先检查 Builder 保存的 Schema 中 `servers[0].url`。它必须是公网 HTTPS origin：

```json
{
  "servers": [
    { "url": "https://mygpt-caddy-sg.202820.xyz" }
  ]
}
```

不能是 `http://127.0.0.1:8787`。这里的 localhost 指 OpenAI 执行 Action 的环境，不是你的 VPS。

Builder 可能保留旧 Action/Schema 缓存。处理顺序：删除旧 Action、保存 GPT、重新添加 Action、重新导入当前 `/openapi.json`、重新设置 Bearer token，再保存 GPT。当前服务响应带 `X-Request-Id`，所有非健康检查请求会在 systemd 日志记录 status，但绝不记录 token。

## Builder 无法导入 Schema

检查：

```bash
curl -sS https://你的域名/openapi.json | jq empty
```

确认：

- `servers[0].url` 是 HTTPS 且没有非标准端口。
- `components.schemas` 是对象。
- operationId 唯一且只使用字母、数字、下划线。
- summary/description 没有超过 300 字符。
- 没有把自定义 Header 写成 `parameters`。
- `openaiFileIdRefs` 是字符串数组 Schema。

## 每次 POST 都弹确认

检查 operation 是否显式包含：

```json
"x-openai-isConsequential": false
```

官方默认所有非 GET 操作为 consequential；缺少字段会导致 POST 默认需要确认。

## `An error occurred while executing the plugin/action`

常见原因：

- 命令超过 45 秒。
- Caddy、DNS 或证书问题。
- 请求或响应超过 100,000 字符。
- Caddy 上游响应头超时太短。
- VPS 防火墙拒绝 OpenAI 请求。

本项目将命令上限设为 38 秒，并在超时杀掉进程组；大型 stdout/stderr 会自动转成文本附件。

## 收到 `ResponseTooLargeError`

不要让命令直接打印完整日志、二进制或大量 JSON。使用过滤和摘要：

```bash
journalctl -n 200 --no-pager
find . -maxdepth 2 -type f | head -200
```

服务超过 30,000 字符会用 `openaiFileResponse` URL；单文件超过 10 MB 会被截断并标记 `output_truncated: true`。

## `openaiFileIdRefs` 没有文件对象

如果请求中收到的是字符串 ID，而不是包含 `download_link` 的对象，服务会返回 `400`。这是为了避免猜测下载地址。重新在对话中附加文件并重试；OpenAI 社区已有文件引用没有自动展开的已知问题报告。

## 返回 401

检查：

```bash
sudo awk -F= '$1 == "API_TOKEN" { print "API_TOKEN is configured" }' /etc/mygpt-caddy.env
```

确认 Builder 使用 Bearer 认证，而不是把 token 当作自定义 Header。若启用了 `ALLOWED_GPT_IDS`，还要确认请求中的 `Openai-Gpt-Id` 在白名单中。

## 返回 400

通常是协议或输入问题：

- JSON 无效；
- body 带未知字段；
- command 为空；
- workdir 不存在；
- `download_link` 不是 HTTPS 或不在 `ALLOWED_UPLOAD_HOSTS`；
- 上传文件超过大小限制。

## HTTP 200 但 `exit_code` 非零

这是预期行为。Action 不应把命令编译失败、测试失败或 Shell `exit 1` 当成服务 500。查看：

```json
{
  "exit_code": 1,
  "stderr": "具体错误",
  "timed_out": false
}
```

让 GPT 根据 stderr 修正命令即可。

## 工作目录没有保持

会话目录依赖：

- `Openai-Conversation-Id`；
- `Openai-Ephemeral-User-Id`。

两个 Header 都缺失时服务使用默认 `WORKSPACE_ROOT`，不会建立可恢复会话。命令显式提供 `workdir` 时，该目录优先级最高。

## HTTPS 失败或只有 80 端口

检查：

```bash
sudo caddy validate --config /etc/caddy/Caddyfile
sudo systemctl reload caddy
ss -ltnp | rg ':80|:443'
curl -Iv https://你的域名/health
```

Caddy site block 必须使用域名而不是 `http://` 前缀，并且 DNS 必须指向 VPS。OpenAI Action 不支持带 `:8787` 的公网 URL。
