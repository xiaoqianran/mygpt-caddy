# 部署与日常运维

## 当前生产拓扑

```text
公网域名：mygpt-caddy-sg.202820.xyz
HTTPS：Caddy / 443
反代：127.0.0.1:8787
服务：mygpt-caddy.service
二进制：/usr/local/bin/mygpt-caddy
配置：/etc/mygpt-caddy.env
状态：/var/lib/mygpt-caddy
```

当前 Caddy 会自动申请公共 CA 证书，使用 zstd/gzip，并把上游响应头超时设为 40 秒；Go 命令执行器最多 38 秒，给 Action 的 45 秒网络窗口留出余量。

## 配置项

最小配置：

```env
API_TOKEN=long-random-secret
ACTION_BASE_URL=https://action.example.com
LISTEN_ADDR=127.0.0.1:8787
WORKSPACE_ROOT=/root
STATE_DIR=/var/lib/mygpt-caddy
COMMAND_TIMEOUT_SECONDS=38
INLINE_OUTPUT_CHARS=30000
MAX_ARTIFACT_BYTES=10000000
MAX_INPUT_FILE_BYTES=10000000
ARTIFACT_TTL_SECONDS=900
ALLOWED_UPLOAD_HOSTS=.oaiusercontent.com
AUDIT_ENABLED=true
AUDIT_DIR=/var/lib/mygpt-caddy/audit
AUDIT_RETENTION_DAYS=30
AUDIT_FSYNC=true
AUDIT_OUTPUT_CHARS=4000
```

可选：

```env
ALLOWED_GPT_IDS=g-xxxxxxxx
```

`ACTION_BASE_URL` 必须是 HTTPS origin，不能带路径。它用于生成 OpenAPI server URL 和大输出下载 URL。

## 首次部署

```bash
make check build
sudo install -m 0755 bin/mygpt-caddy /usr/local/bin/mygpt-caddy
sudo install -m 0755 bin/mygpt-audit /usr/local/bin/mygpt-audit
sudo install -m 0644 deploy/mygpt-caddy.service /etc/systemd/system/mygpt-caddy.service
sudo install -m 0600 deploy/mygpt-caddy.env.example /etc/mygpt-caddy.env
sudo systemctl daemon-reload
sudo systemctl enable --now mygpt-caddy
```

Caddy：

```bash
sudo install -d -o caddy -g caddy -m 0750 /var/log/caddy
sudo install -o caddy -g caddy -m 0600 /dev/null /var/log/caddy/mygpt-caddy-access.json
sudo caddy validate --config /etc/caddy/Caddyfile
sudo systemctl reload caddy
```

先创建入口日志并设置 `caddy:caddy` 属主。否则以 root 执行 `caddy validate` 可能先创建 root-only 文件，导致 caddy 服务启动时报 `permission denied`。

## Custom GPT Builder

1. 导入 `https://你的域名/openapi.json`。
2. Authentication 选择 API Key。
3. 类型选择 Bearer。
4. 填写 `/etc/mygpt-caddy.env` 中的 `API_TOKEN`。
5. 首次测试使用 `pwd && hostname`。

本地验证：

```bash
curl -sS https://action.example.com/health
curl -sS https://action.example.com/openapi.json | jq .
```

## 发布新版本

```bash
make check build
sudo install -m 0755 bin/mygpt-caddy /usr/local/bin/mygpt-caddy
sudo install -m 0755 bin/mygpt-audit /usr/local/bin/mygpt-audit
sudo systemctl restart mygpt-caddy
sudo systemctl is-active mygpt-caddy caddy
```

不要把 `/etc/mygpt-caddy.env` 提交到 Git；仓库的 `.gitignore` 已忽略 `*.env`。

## 回滚

升级前先保存当前二进制：

```bash
sudo cp -p /usr/local/bin/mygpt-caddy /usr/local/bin/mygpt-caddy.previous
```

发生启动故障时：

```bash
sudo install -m 0755 /usr/local/bin/mygpt-caddy.previous /usr/local/bin/mygpt-caddy
sudo systemctl restart mygpt-caddy
sudo journalctl -u mygpt-caddy -n 80 --no-pager
```

## 诊断命令

```bash
systemctl status mygpt-caddy --no-pager
journalctl -u mygpt-caddy -n 100 --no-pager
ss -ltnp | rg ':8787|:443'
curl -sS http://127.0.0.1:8787/health
curl -sS https://action.example.com/health
caddy validate --config /etc/caddy/Caddyfile
mygpt-audit verify
mygpt-audit -limit 50 recent
```

## 防火墙与 OpenAI egress

OpenAI 官方说明 Action 请求来自公开的 egress IP 范围。若 VPS 使用严格防火墙，应定期从官方页面更新 allowlist；最简单的 Action 兼容做法是只限制服务监听在本机、让 Caddy 暴露标准 443，并在防火墙层允许 HTTPS。

不要把历史 IP 列表永久硬编码进项目，因为官方范围可能变化。
