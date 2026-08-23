# OpenAI Actions 兼容性

## 官方约束与本项目实现

| 官方约束 | 本项目策略 |
| --- | --- |
| API 往返超时 45 秒 | 命令服务端最多运行 38 秒，并杀掉整个 POSIX 进程组 |
| 请求体和响应体均小于 100,000 字符 | 请求上限 99,000 字节；输出超过 30,000 字符改走附件 URL |
| TLS 1.2+，公网端口 443，有效公共证书 | Caddy 负责 HTTPS 和 Let's Encrypt 证书；Go 只监听 `127.0.0.1:8787` |
| endpoint 的 `summary`/`description` 最多 300 字符 | OpenAPI 中所有 operation 描述保持在 300 字符以内 |
| API 参数 `description` 最多 700 字符 | OpenAPI 参数描述保持简短，并由测试检查 operation 描述长度 |
| 不支持自定义 Header 参数 | Schema 不声明 `in: header` 参数；Bearer 走标准 security scheme |
| `x-openai-isConsequential` 控制确认 | `runCommand` 显式设为 `false`，允许用户选择 Always allow |
| 非 GET 默认 consequential，GET 默认非 consequential | 所有会改变 VPS 状态的 POST 都显式声明 `false`，避免隐式默认值 |
| 文件回传最多 10 个、每个最多 10 MB | 只产生 stdout/stderr 文本附件，每个最大 10,000,000 字节 |
| URL 文件下载超时 10 秒 | 下载客户端超时 10 秒，并检查 `Content-Type`/`Content-Disposition` |
| Action 返回文件不能是图片或视频 | 输出附件固定为 `text/plain; charset=utf-8` |

官方页面还说明：ChatGPT 会根据短时间内的 429/500 动态退避。因此业务命令失败不返回 500，而是在 HTTP 200 中返回 `exit_code` 和 `stderr`，让 GPT 有机会自我修复。

## Schema 设计

当前 Schema 只有一个 operation：`runCommand`。这样可以让 Builder 和模型只处理一个稳定的命令入口，扩展能力放在请求字段和服务端实现中。

Schema 采用以下兼容写法：

- `openapi: 3.1.0`。
- 全局唯一、只含字母数字下划线的 `operationId`。
- `components.schemas` 始终是对象，即使有复用定义。
- 使用普通 object/array/string/integer；避免深层 `allOf`、`anyOf`、`oneOf`。
- 使用标准 Bearer security scheme，不在 parameters 中伪造自定义鉴权 Header。
- `openaiFileIdRefs` 在 Schema 中声明为 `array` of `string`，因为官方文档说明运行时会展开为 JSON 对象数组。
- `openaiFileResponse` 使用 URL 数组，避免 Base64 文件把响应体推过 100k 限制。

入口文件：[internal/agent/openapi.json](../internal/agent/openapi.json)。服务的 `/openapi.json` 会把 `{{ACTION_BASE_URL}}` 渲染为真实 HTTPS origin。

## 请求与响应语义

### 成功或业务失败：HTTP 200

```json
{
  "exit_code": 1,
  "stdout": "",
  "stderr": "go test: failed",
  "timed_out": false,
  "output_truncated": false,
  "duration_ms": 423,
  "workdir": "/root/project"
}
```

`exit_code != 0` 是 Shell 业务结果，不是 Action 协议错误。

### 协议或鉴权错误

- `400`：JSON 损坏、未知字段、空 command、非法 workdir、文件引用没有被 ChatGPT 展开。
- `401`：缺少或错误的 Bearer token，或配置了 `ALLOWED_GPT_IDS` 但 GPT ID 不在白名单。
- `404`：不存在的下载附件或未知路径。
- `410`：签名附件已过期。

## 官方明确与未确认说法

下面这些是官方 Production notes 明确写出的限制：45 秒、100k、TLS/443、描述长度、consequential 默认行为、429/500 退避和文件规则。

下面这些在当前官方页面没有被确认成稳定契约，因此本项目不把它们写成硬性依赖：

- 每个 Action 最多 30 个 operation。
- 同一个 Custom GPT 对同一域名只能导入一个 Schema。
- `Openai-Conversation-Id`、`Openai-Ephemeral-User-Id`、`Openai-Gpt-Id` 等 Header 的长期稳定性。
- 某个固定的 OpenAI egress IP 集合不会变化。

服务会兼容地读取这些 Header；缺失时命令仍可无状态运行。若使用防火墙 allowlist，应以 OpenAI 当前公开 IP 页面为准，而不要把旧列表永久写死。

## 参考来源

- [Production notes on GPT Actions](https://developers.openai.com/api/docs/actions/production)
- [Sending and returning files with GPT Actions](https://developers.openai.com/api/docs/actions/sending-files)
- [Getting started with GPT Actions](https://developers.openai.com/api/docs/actions/getting-started)
- [OpenAI Developer Forum：`openaiFileIdRefs` 已知问题](https://community.openai.com/t/openaifileidrefs-not-auto-populated-in-action-call-createmap-publishing-fails/1374402/4)
- [AI Server Commander：边界执行、进程组终止与结构化结果](https://github.com/Jhacarreiro/ai-server-commander)
- [gpt-actions：可导入 Schema 示例集合](https://github.com/agisota/gpt-actions)

