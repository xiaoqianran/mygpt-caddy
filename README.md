# mygpt-caddy

一个供 Custom GPT Action 调用的轻量 Go 命令执行引擎。Caddy 负责公网 HTTPS，服务只监听 `127.0.0.1:8787`。

详细参考见 [docs/](./docs/README.md)：包含官方兼容性矩阵、架构、双向文件处理、部署运维、故障排查、安全模型和全链路审计。

## 设计

- 一个 `runCommand` operation，保持 OpenAPI 与模型调用简单稳定。
- 请求总时限最多 38 秒；超时会终止整个 POSIX 进程组。
- 按 `Openai-Conversation-Id` 持久化当前目录，并用临时用户 ID 隔离会话。
- 注入非交互环境，避免包管理器、pager 和 Git 凭据提示挂起。
- 30,000 字符以内直接返回；更大输出通过短期签名 URL 作为 `openaiFileResponse` 文本附件返回。
- 接收 `openaiFileIdRefs`，并立即下载到临时目录；命令通过 `$OPENAI_FILE_DIR` 和 `$OPENAI_FILE_PATHS_JSON` 使用文件。
- Bearer 鉴权；可选 `ALLOWED_GPT_IDS`；上传下载域名默认仅允许 `*.oaiusercontent.com`。
- Shell 退出码非零仍返回 HTTP 200，方便 GPT 读取错误并修正；协议错误使用 400，鉴权错误使用 401。
- Caddy 公网入口日志与 Go JSONL 审计链共享 `trace_id`；记录鉴权、下载、执行、输出与失败阶段，并提供独立校验 CLI。

> 这是远程 shell，不是沙箱。当前部署以 root 运行，Bearer token 等同于 VPS root 权限。只连接你信任的私有 GPT，并使用长随机 token。

## 部署

```bash
make check build
sudo install -m 0755 bin/mygpt-caddy /usr/local/bin/mygpt-caddy
sudo install -m 0755 bin/mygpt-audit /usr/local/bin/mygpt-audit
sudo install -m 0644 deploy/mygpt-caddy.service /etc/systemd/system/mygpt-caddy.service
sudo install -m 0600 deploy/mygpt-caddy.env.example /etc/mygpt-caddy.env
sudo systemctl daemon-reload
sudo systemctl enable --now mygpt-caddy
```

编辑 `/etc/mygpt-caddy.env`，至少设置随机 `API_TOKEN` 和真实 `ACTION_BASE_URL`。将 `deploy/Caddyfile.example` 的域名替换为你的域名，合并到现有 Caddyfile 后执行：

```bash
sudo caddy validate --config /etc/caddy/Caddyfile
sudo systemctl reload caddy
```

## 配置 Custom GPT

1. 在 Builder 中导入 `https://你的域名/openapi.json`。
2. Authentication 选择 API Key、Bearer，并填入 `/etc/mygpt-caddy.env` 中的 `API_TOKEN`。
3. 先测试 `pwd && hostname`。

示例：

```bash
curl -sS https://action.example.com/v1/command/run \
  -H "Authorization: Bearer $API_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"command":"pwd && hostname"}'
```

上传文件时，命令可使用：

```bash
printf '%s' "$OPENAI_FILE_PATHS_JSON" | jq -r '.[].path'
unzip "$OPENAI_FILE_DIR/app.zip" -d ./app
```

## OpenAI Actions 约束

实现按官方当前文档收敛：API 往返 45 秒、请求/响应各小于 100,000 字符、TLS 1.2+ 且仅 443、endpoint 描述 300 字符以内、参数描述 700 字符以内。文件返回最多 10 个、每个 10 MB，URL 拉取超时 10 秒；返回文件不得是图片或视频。

参考：

- [Production notes on GPT Actions](https://developers.openai.com/api/docs/actions/production)
- [Sending and returning files with GPT Actions](https://developers.openai.com/api/docs/actions/sending-files)
- [Getting started with GPT Actions](https://developers.openai.com/api/docs/actions/getting-started)
- [OpenAI Developer Forum：文件引用已知问题](https://community.openai.com/t/openaifileidrefs-not-auto-populated-in-action-call-createmap-publishing-fails/1374402/4)
- [AI Server Commander：边界执行与进程组终止实践](https://github.com/Jhacarreiro/ai-server-commander)
- [gpt-actions：简洁、可导入 Schema 实践](https://github.com/agisota/gpt-actions)

社区项目常见的边界执行、进程组终止、结构化结果和窄 REST surface 也被保留；文件引用功能仍可能遇到 Builder/平台侧已知问题，服务会对未展开的字符串引用返回可读的 400，而不是误执行命令。

官方当前页面没有确认“最多 30 个 operation”“每个 GPT 同域名只能导入一个 Schema”以及这些 OpenAI 元数据 Header 的稳定性，因此本项目不把它们描述为官方契约。实现仍只暴露一个 operation，并将 Header 缺失视为可兼容的无状态调用。
