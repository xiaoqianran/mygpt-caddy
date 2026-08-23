# 双向文件处理

## 方向一：ChatGPT 上传文件到 VPS

OpenAI Action 的 Schema 使用：

```json
{
  "openaiFileIdRefs": {
    "type": "array",
    "items": { "type": "string" },
    "description": "Up to 10 files from this conversation needed by the command."
  }
}
```

官方说明：虽然 Schema 中是字符串数组，运行时会变成对象数组：

```json
[
  {
    "name": "app.zip",
    "id": "file-abc",
    "mime_type": "application/zip",
    "download_link": "https://files.oaiusercontent.com/..."
  }
]
```

`download_link` 是临时链接，官方文档说明有效期约 5 分钟。服务收到后立即并行下载，不保存 OpenAI token，也不把链接写入会话状态。

## `ALLOWED_UPLOAD_HOSTS` 是什么

```env
ALLOWED_UPLOAD_HOSTS=.oaiusercontent.com
```

这是下载 `download_link` 时的域名白名单：

```text
.oaiusercontent.com
├── 允许 oaiusercontent.com
├── 允许 files.oaiusercontent.com
├── 允许其他子域名.oaiusercontent.com
└── 拒绝 evil-oaiusercontent.com
```

它不是：

- CORS 配置；
- Bearer 鉴权；
- OpenAI Action egress IP 白名单；
- 文件扩展名白名单；
- `openaiFileResponse` 输出附件的域名配置。

它的核心作用是阻止 SSRF：模型传入的 URL 不能指向 `localhost`、内网 IP、云元数据地址或任意第三方站点。

### 下载检查

实现位于 [uploads.go](../internal/agent/uploads.go)：

- 最多 10 个文件。
- URL 必须是 HTTPS。
- 初始 URL 和每次重定向都必须匹配白名单。
- 最多跟随 3 次重定向。
- 单文件最多 10 MB。
- HTTP 客户端超时 10 秒。
- 文件名经过 `filepath.Base` 和字符清洗，防止 `../../` 路径穿越。
- 临时目录 `0700`，文件 `0600`。
- 命令结束后删除本次输入文件目录。

可扩展配置示例：

```env
ALLOWED_UPLOAD_HOSTS=.oaiusercontent.com,downloads.example.com
```

带前导点的条目允许主域名及子域名；不带前导点的条目只允许精确主机名。

## 命令如何使用上传文件

服务将文件写入唯一临时目录，并注入：

```bash
echo "$OPENAI_FILE_DIR"
printf '%s' "$OPENAI_FILE_PATHS_JSON" | jq -r '.[].path'
unzip "$OPENAI_FILE_DIR/app.zip" -d ./app
```

`OPENAI_FILE_PATHS_JSON` 是 JSON 字符串，不是文件名；用 `printf` 送给 `jq`，不要把它直接当作 `jq` 的路径参数。

## 方向二：VPS 输出文件回传 ChatGPT

官方支持 inline Base64 和 URL 两种方式。生产实现使用 URL：

```json
{
  "stdout": "Full output is attached.",
  "openaiFileResponse": [
    "https://action.example.com/v1/files/download/<id>/<name>?expires=...&signature=..."
  ]
}
```

服务行为：

1. stdout 和 stderr 分别写入受限 `.log` 文件。
2. 总输出超过 30,000 字符时不把全文塞进 JSON。
3. 每个文件最多 10,000,000 字节。
4. URL 使用 HMAC、附件 ID、文件名和过期时间签名。
5. 下载时设置 `Content-Type: text/plain; charset=utf-8`。
6. 下载时设置 `Content-Disposition: attachment`。
7. 附件默认 15 分钟过期，后台每分钟清理。
8. 服务重启后旧的进程内附件登记失效，磁盘残留会在启动时清理。

官方规定 Action 返回文件最多 10 个、每个最多 10 MB，并且不能是图片或视频；本项目只回传文本日志，避免把不受支持的类型交给 Action。

## 文件引用故障的实际处理

OpenAI Developer Forum 有关于某些文件上传/下载流程中 `openaiFileIdRefs` 没有自动展开、导致发布或调用失败的报告。服务检测到字符串没有展开时返回明确的 `400`，不会把字符串误当作下载 URL 或执行参数。

参考：[Developer Forum 已知问题](https://community.openai.com/t/openaifileidrefs-not-auto-populated-in-action-call-createmap-publishing-fails/1374402/4)。

