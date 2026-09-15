# Runner 端到端流式调用

从 `cmd/stream-demo` 开始：

```bash
go run ./cmd/stream-demo
```

脚本模型分两轮运行，中间实际调用加法工具，无需 API key。现有 OpenAI Chat Completions Client 已实现 model.Streamer，可替换脚本模型；本轮用本地 HTTP 服务验证取消传播，没有调用真实模型服务。

## 使用入口

Runner.Run 保持非流式行为。Runner.Stream 要求策略实现可选 StreamingAgent，ReAct 已实现；模型还须实现 model.Streamer。不支持的策略在 Stream 返回 ErrStreamUnsupported；ReAct 的模型不支持时，通过 Recv/Wait 返回该错误，不会悄悄回退 Generate。

以下为函数内调用片段，需导入 context、errors、io 及项目包：

```go
stream, err := runner.Stream(ctx, agent.RunRequest{
    Messages: input,
    Timeouts: agent.Timeouts{Run: time.Minute, Model: 20*time.Second},
    Hook: hook,
})
if err != nil { return err }
defer stream.Close()

for {
    content, readErr := stream.Recv()
    if errors.Is(readErr, io.EOF) { break }
    if readErr != nil { break } // 原因及部分结果仍从 Wait 取
    // 按 Step + Event.BlockID 跟踪块类型，仅显示文本 delta。
    render(content)
}
result, runErr := stream.Wait()
// runErr 非 nil 时也检查 result.ToolBatches、History、StopReason。
```

## 内容与执行事件

- ContentEvent 包含 RunID、内容序号、Step 和 msg.StreamEvent。Sequence 只给内容编号，与 Hook 事件序号分别计数，不能按两个序号拼全局顺序。
- BlockID 仅在一轮模型回复内唯一，UI 应使用 RunID + Step + BlockID。BlockStart 提供类型，Delta 可能是文本、思考或工具 JSON，不要一律当回答展示。
- Hook 保持运行/模型/工具的只读进度通知；内容流不进入 Hook。两条通道的消费时机不同，Hook 的 RunFinished 可能先于 UI 清空缓冲区。
- 每项内容在发送前深复制，消费者修改块不会影响聚合或最终结果。未完成参数保留原始字节，不能用 Msg.Clone 强行验证半个 JSON。
- 所有内容都属于暂态。即使看到 BlockEnd，也要等待整轮模型 Stream 成功、事件聚合合法且与返回消息一致，ReAct 才会执行工具。失败流中的片段不会进入正式 History。
- OpenAI 适配器目前实时转发文本，工具参数在协议流结束后发送归一化块；Demo 展示的分片接口能力不等于该适配器实时转发网络上的每个参数片段。

## 读取、等待和关闭

一个 Stream 对应一个生产 worker，单消费者调用 Recv。成功读完返回 io.EOF；失败先允许读取已入队片段，随后返回运行错误。Wait 等待完整收尾，返回非 nil Result 和最终错误；可重复调用，但返回同一指针，不要并发修改。

**读到终止再 Wait，或先 Close 再 Wait。** 队列有 16 个事件的容量，单项内部编码包限 1 MiB。慢速读取会反压同步模型回调。直接停止读取再 Wait，生产者可能正在等队列空位，双方会互等；defer Close 也不能解救尚未返回的 Wait。

Close 幂等：取消并等待 worker 退出。已完成的结果不会因之后 Close 而变成取消。主动取消通过 Wait 读取原因，Close 本身正常完成返回 nil。不要在该运行的同步 Hook 中调用 Close/Wait，会等待自己。Hook 不应阻塞。

Close 和模型/运行 deadline 能解除队列发送阻塞。自定义策略、模型和工具必须配合 context，回调错误必须向上传播，返回前退出自有工作。框架无法强制终止忽略 context 的代码；Close 也会等待这样的组件。

缓冲限制不代表整个任务内存有上限：聚合的完整消息、历史和工具输出仍会增长。本轮没有总 token/字节预算。

## 结果与兼容

流式和非流式共用 Runner.run 和 ReAct.execute。Steps 计实际启动的模型调用次数（Generate 或 Stream），不是片段数。运行结束只通知一次。取消时已完成工具结果保留，unknown 与 not_started 沿用前一版语义，History 仍可能无法直接恢复。

Stream 创建前检查上下文非 nil、策略能力并复制输入；其余执行校验错误由运行结果返回。没有创建 Stream 的前置失败不会产生生命周期事件。同步 panic 沿用 Runner 契约：返回通用错误，不能恢复未返回的局部结果。

## 验证

stream_test.go 覆盖两轮工具调用、半截/非法 JSON、模型流失败、最终消息不一致、内容隔离、队列反压、模型/运行超时、重复 Close、单项大小限制、不支持能力及工具取消后的部分结果。

stream_http_test.go 用本地 SSE 服务验证 Close 后服务端请求 context 取消。原 Runner、ReAct 和工具用例继续全量回归。
