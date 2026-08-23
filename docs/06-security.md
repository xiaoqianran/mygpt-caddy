# 安全模型与生产加固

## 当前信任模型

`mygpt-caddy` 是远程 Shell，不是沙箱。当前 VPS 服务以 root 运行，因此：

```text
Bearer token 泄露
       ≈
VPS root shell 泄露
```

只应把 Action 连接到你信任的 Custom GPT，并把 token 当作 root 凭据管理。

## 已实现的防护

- Bearer token 使用常量时间比较。
- 请求体上限约 99,000 字节。
- JSON 使用 `DisallowUnknownFields`。
- 命令最长 38 秒。
- 超时杀整个进程组，不只杀 Bash 父进程。
- stdout/stderr 每个最多 10 MB。
- 上传文件必须走 HTTPS 和允许域名。
- 重定向重新执行域名检查。
- 上传临时目录和文件使用收紧权限。
- 文件名经过 basename 和字符清洗。
- 输出下载 URL 使用 HMAC、过期时间和进程内附件登记。
- 下载附件只允许登记过的 ID 和文件名。
- OpenAPI 不暴露任意自定义 Header 参数。

## SSRF 边界

攻击者可能尝试把 `download_link` 指向：

```text
http://127.0.0.1:2019/
http://169.254.169.254/
file:///etc/passwd
https://evil.example/
```

这些会被拒绝，因为实现要求 HTTPS，并且 host 必须匹配 `ALLOWED_UPLOAD_HOSTS`。默认只允许 `.oaiusercontent.com`。

如果增加额外下载域名，应只加入明确可信的精确 host 或受控子域名，不要设置成任意域名。

## 建议的生产加固

1. 使用专用非 root 用户运行 systemd 服务。
2. 将 `WORKSPACE_ROOT` 限制到专用工作目录。
3. 使用短期、长随机 Bearer token，泄露后立即轮换。
4. 不把 `/etc/mygpt-caddy.env`、日志附件或会话状态提交到 Git。
5. 必要时配置 `ALLOWED_GPT_IDS`。
6. 在 Caddy 和防火墙层只开放 443。
7. 定期检查 systemd 日志和附件目录大小。
8. 对命令能力增加业务层 denylist 或审批流程，而不是依赖 GPT 自觉。
9. 如果需要处理不可信脚本，使用容器、VM 或专门沙箱；当前实现不会伪装成沙箱。

## 凭据与公开仓库

仓库是公开的，源码和示例配置不得包含：

- `API_TOKEN` 实际值；
- GitHub/OpenAI token；
- 生产环境变量文件；
- 私钥、Cookie、签名 URL；
- 真实用户文件和日志。

公开仓库并不影响服务端 `/etc/mygpt-caddy.env` 的机密性，但一旦 token 被提交到 Git 历史，必须立即撤销并轮换。

