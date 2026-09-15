# Anthropic Messages API 适配

实现 `model.Model`、`model.Streamer`、`model.Formatter`，接入原有 ReAct / Runner，无需修改工具或核心循环。

## 快速验证

```bash
go run ./cmd/anthropic-demo
go run ./cmd/anthropic-demo -stream
```

默认使用内存 HTTP fixture，不联网、不需要 API key。fixture 生成工具调用与最终回复，工具函数实际计算加法。它验证适配链路，不代表真实模型行为。更严格的测试会断言第二轮请求确实包含工具结果 42。

普通模式输出：

```text
stop=completed steps=2 answer="20 + 22 = 42"
```

流式模式输出：

```text
step=1 delta="{\"a\":20,\"b\":22}"
step=2 delta="20 + 22 = 42"
stop=completed steps=2 answer="20 + 22 = 42"
```

真实调用需先在本地环境配置 `ANTHROPIC_API_KEY` 与 `ANTHROPIC_MODEL`（使用你账号可用的模型 ID），再显式执行：

```bash
go run ./cmd/anthropic-demo -real
go run ./cmd/anthropic-demo -real -stream
```

真实调用会产生供应商费用。本轮没有读取用户密钥，也没有执行 `-real`。

## 接入代码

以下为应用内片段：

```go
client, err := anthropic.New(anthropic.Config{
    APIKey: os.Getenv("ANTHROPIC_API_KEY"),
    Model: os.Getenv("ANTHROPIC_MODEL"),
    MaxTokens: 4096,
})
if err != nil { return err }
react, err := agent.New(client, registry, 8)
if err != nil { return err }
runner, err := agent.NewRunner(react)
if err != nil { return err }
result, err := runner.Run(ctx, agent.RunRequest{Messages: history})
```

流式入口沿用 `Runner.Stream`，读取、Close/Wait 的所有权约定见 [流式文档](../../agent/STREAMING.md)。单次模型和工具超时仍由 RunRequest.Timeouts 设置。

## 配置与协议

- 默认 BaseURL 为 `https://api.anthropic.com/v1`，请求路径为 `/messages`。自定义 URL 应包含 `/v1`；HTTP 仅允许 loopback，禁止 URL 中的账号、查询参数、fragment。
- 使用 `x-api-key` 与 `anthropic-version: 2023-06-01`；不会跟随重定向以免转发凭据。
- MaxTokens 为 0 时默认 4096，负值拒绝。此适配器的 0 表示默认值，不支持 API 的 cache-only 请求。实际模型的上下限由服务端进一步校验。
- 默认 HTTP 超时 60 秒。传入 HTTPClient 时复制客户端值，使用其 Timeout/Transport；不自动重试。
- 非 2xx 返回 `*HTTPError`，包含状态码和 request-id；不回显供应商原始错误正文。error SSE 返回通用错误。取消保留 context 错误链。

## 当前支持范围

| 内容 | 行为 |
|---|---|
| 前置系统文本 | 转为顶层 system 内容数组 |
| 用户/助手文本 | 保持内容块顺序，连续同角色消息合并 |
| 用户图片、工具结果图片 | 支持 URL 或 JPEG/PNG/GIF/WebP base64 |
| 客户端工具定义 | name/description/input_schema，对象 schema 校验 |
| 工具调用 | tool_use，参数直接保留 JSON 对象字节，避免数字经过 float64 |
| 工具结果 | user 中的 tool_result，保留 tool_use_id 与 is_error |
| 完成原因 | 接受 end_turn、tool_use，检查与工具块是否一致 |
| 流式内容 | 文本及 input_json_delta，聚合为完整 Msg |

会话必须以 user 输入开始，以 user/tool 输入结束；中途 system、assistant prefill、音频、thinking/redacted_thinking、文档、服务端工具、缓存控制和其他 beta 参数暂不支持。这里是本适配器的边界，不能据此推断供应商没有这些能力。

`max_tokens`、`pause_turn`、`refusal`、未知停止原因返回错误，不把截断消息送去执行工具。未知输出块或流事件也明确报错，不静默丢弃潜在语义。后续协议增加能力时需要更新适配器。

## 流式校验

要求 message_start → 有序内容块 start/delta/stop → message_delta → message_stop。支持 ping、多个 message_delta、累计 usage、CRLF 和多 data 行；EOF 不等于完成。内容索引必须连续，当前按逐块顺序解析。

工具 start 的 input 须为空对象，后续增量按 JSON 原始文本拼接。零参数工具没有增量时补空对象。参数在 block_stop 时验证，整轮到 message_stop 才返回完成 Response；ReAct 还会检查事件与最终消息一致，再执行工具。回调错误立即停止读取，并通过 defer 关闭 HTTP body。

普通响应与 SSE 总读取限制 8 MiB，单行 SSE 上限 1 MiB。Runner 内容队列的限制另见流式文档；这些不是模型上下文窗口或整个任务内存预算。

## 用量映射

通用 `Usage.PromptTokens = input_tokens + cache_creation_input_tokens + cache_read_input_tokens`；CompletionTokens 对应 output_tokens，TotalTokens 为二者之和。流式 message_delta 的用量是累计值，覆盖此前字段而非累加；缺失或 null 字段保留已有值。

通用 Usage 不表达缓存细分价格，也不保留细分缓存计数。它不能直接当费用账单。

## 已验证与未验证

本地 `go test -race ./...`、`go vet ./...` 通过，包括：两轮普通/流式 Runner 集成，工具参数精度和格式转换，图片映射，非法参数不执行工具，截断流/错误事件/索引异常，累计用量，HTTP 错误与重定向拒绝，消费者错误、提前关闭后的服务端取消。

默认 Demo 通过。真实账号权限、模型可用性、供应商限流与真实多模态理解效果尚未实测。

协议依据：[Messages API](https://platform.claude.com/docs/en/api/messages/create)、[流式事件](https://platform.claude.com/docs/en/build-with-claude/streaming)、[用量及流类型](https://platform.claude.com/docs/en/api/typescript/messages)。实现仅覆盖上面列明的子集。
