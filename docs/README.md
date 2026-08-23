# 文档索引

这些文档记录 `mygpt-caddy` 的设计依据、OpenAI Actions 兼容边界、文件传输、安全模型和 VPS 运维方法。

- [OpenAI Actions 兼容性](./01-openai-actions-compatibility.md)：官方限制、Schema 写法、Builder 潜规则与未确认说法。
- [系统架构与请求生命周期](./02-architecture.md)：Caddy、Go 服务、会话、执行器和响应分流。
- [双向文件处理](./03-file-handling.md)：`openaiFileIdRefs`、`openaiFileResponse` 和 `ALLOWED_UPLOAD_HOSTS`。
- [部署与日常运维](./04-deployment-operations.md)：systemd、Caddy、配置、验证和回滚。
- [故障排查](./05-troubleshooting.md)：Builder、超时、附件、鉴权、TLS 和命令错误。
- [安全模型](./06-security.md)：root 执行风险、SSRF 防护、凭据管理和生产加固建议。
- [Custom GPT 描述与指令](./07-gpt-description-and-instructions.md)：可直接粘贴到 Builder 的精简版本。
- [全链路追溯与审计](./08-audit-tracing.md)：Caddy 入口、Go 阶段事件、文件与命令追踪、哈希链和查询 CLI。

官方来源以 [OpenAI Production notes](https://developers.openai.com/api/docs/actions/production) 和 [Sending and returning files](https://developers.openai.com/api/docs/actions/sending-files) 为准；社区讨论用于补充踩坑，不替代官方契约。
