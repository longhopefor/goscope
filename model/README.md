# 第 2 篇：Model 接口与 Formatter

本篇把 `msg.Msg` 接到模型 HTTP API：

```text
model.Request → Formatter.Encode → HTTP → Formatter.Decode → model.Response
                                      SSE → msg.Aggregator → 完整 Msg
```

## 代码位置

- `model.go`：厂商无关的 Model、Streamer、Formatter，以及工具定义和响应用量。
- `openai/formatter.go`：Chat Completions 请求与响应转换，没有网络操作。
- `openai/client.go`：HTTP、鉴权、超时和错误处理。
- `openai/stream.go`：SSE 分帧、分片合并与消息聚合。
- `openai/openai_test.go`：本地 HTTP 测试、格式转换和异常路径。
- `../cmd/model-demo/`：默认本机模拟、可显式切换真实调用。

## 运行

仓库根目录，Go 1.24+，仅标准库：

```sh
go test -race ./...
go vet ./...
go run ./cmd/model-demo
go run ./cmd/model-demo -stream
```

默认启动 httptest 本地 HTTP 服务，经过真实 HTTP 客户端及 Formatter，但返回固定内容；不是模型实测。

真实调用需在本机设置 `OPENAI_API_KEY` 和 `OPENAI_MODEL`。模型名须是账号可用且支持 Chat Completions 的名称。可选 `OPENAI_BASE_URL` 默认 `https://api.openai.com/v1`；自定义地址需包含 `/v1` 或对应服务前缀，客户端追加 `/chat/completions`。密钥只通过环境变量读取，不要写进代码或提交 Git。

```sh
go run ./cmd/model-demo -live
go run ./cmd/model-demo -live -stream
```

未执行真实模型调用。示例不自动挑选模型、不自动读取 .env，也不执行工具。

## 使用接口

```go
client, err := openai.New(openai.Config{
    APIKey: os.Getenv("OPENAI_API_KEY"),
    Model: os.Getenv("OPENAI_MODEL"),
})
if err != nil { return err }
var llm model.Model = client
response, err := llm.Generate(ctx, model.Request{
    Messages: []*msg.Msg{msg.NewText("user", msg.RoleUser, "你好")},
})
```

工具通过 `Request.Tools` 提供 Name、Description 和对象类型的 JSON Schema。返回的工具调用保存在 `Response.Message.Blocks`，由下一篇的工具执行器处理。响应 ID、模型名、完成原因和 token 用量在 Response 上，不混入用户内容。

## 支持范围与取舍

- 输入：system/user/assistant 文字，user 图片（HTTP(S) URL 或 base64），assistant 函数工具调用，文本工具结果。
- 输出：assistant 文字、函数工具调用；只接受 `stop` / `tool_calls` 完成原因且与内容一致。长度截断、拒绝、格式错误返回错误。
- 音频、推理内容/签名、图文工具结果暂不适配，已知不支持的块明确报错。不要因为 msg 能保存某种块，就认为模型适配器能处理它。
- 工具结果紧随 assistant 工具请求，一条内部工具消息可拆成多条 API tool 消息。历史必须包含完整调用/结果配对。
- 普通多文本块按换行合并；文本在工具调用之后的内部布局无法保序，编码时拒绝。响应统一成文字块在前、工具块在后。
- 不透传内部消息 ID、Name、Metadata 和 Timestamp；它们是内部信息，不作为聊天正文发送。
- Formatter 只验证工具 Schema 的顶层 object 类型，不实现完整 JSON Schema 校验。
- Stream 同步调用回调。文本增量实时发出；工具按 index 收集名称和参数，等 finish_reason 和 `[DONE]` 都到达后，按首次出现顺序发出完整工具块。工具参数不是实时逐片透传。
- 回调可返回错误终止；context 可取消。任何错误都不返回完整 Response，已经发送的事件仅可用于临时显示。不能在回调里提前执行工具。
- SSE 支持 CRLF、注释和多 data 行，单行上限 1 MiB，总体上限 8 MiB；截断不视作成功。
- HTTP 默认 60 秒整体超时；可注入 HTTPClient。禁止重定向，不自动重试，HTTPError 只公开状态和请求 ID，不回显响应正文。
- BaseURL 仅接受 HTTPS 或 HTTP 回环地址。APIKey 会发送到配置地址，使用可信服务。
- 不包括 Responses API、自动工具循环、多 choice、响应图片/音频或所有厂商扩展字段。对 OpenAI-compatible 服务仍需按目标服务实测。

## 来源

2026-09-11 核对官方协议文档：

- [Create chat completion](https://developers.openai.com/api/reference/resources/chat/subresources/completions/methods/create)
- [Chat Completions streaming events](https://developers.openai.com/api/reference/resources/chat/subresources/completions/streaming-events)

选择 Chat Completions 是为了本篇围绕消息和函数工具协议教学；不表示它是所有新项目的默认最佳选择。
