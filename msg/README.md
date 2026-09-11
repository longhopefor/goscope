# 消息包

当前实现采用受控 `[]Block`，块类型由包维护，角色校验不照搬某一家模型 API。
`Msg` 表示完整消息；流式片段单独放在 `Aggregator` 中。暂不在消息层实现工具执行、权限审批或持久化事件日志。

## 创建与校验

```go
request := msg.New("assistant", msg.RoleAssistant,
    msg.ToolUse("call_1", "get_weather", json.RawMessage(`{"city":"北京"}`)),
)
result := msg.New("get_weather", msg.RoleTool,
    msg.ToolResult("call_1",
        msg.Text("演示数据：晴"),
        msg.Image(msg.Source{Kind: "url", URL: "https://example.com/weather.png"}),
    ),
)
err := msg.ValidateConversation([]*msg.Msg{request, result}, true)
```

`New`/`Add` 允许逐步构建，消费前调用 `Validate()`。`Msg` JSON 编解码和 `Clone()` 也执行校验。单独编码 `Block` 不自动验证，需调用其 `Validate()`。

- system：只允许文字。
- user：文字、图片、音频。
- assistant：文字、图片、音频、推理、工具调用。
- tool：只允许工具结果；结果内容只允许文字、图片、音频。
- 完整消息至少有一个块，允许空文本或空工具输出。
- 工具参数必须是完整 JSON 对象，不做具体工具的参数 schema 校验。
- `ValidateConversation` 检查整个传入历史中调用 ID 唯一、结果引用先前调用且不重复；第二个参数决定是否允许尚无结果的调用。它不强制角色交替，也不处理被裁剪掉的历史。
- source 支持 HTTP(S) URL 或标准 base64；内联媒体必须有匹配的 MIME 前缀。检查数据表达，不验证远端资源是否存在或二进制内容是否确为该格式。

## 流式聚合

`NewAggregator(name)` 创建一条 assistant 回复的聚合器。

1. `BlockStart`：携带唯一的流内 BlockID 和文字/推理/工具调用块；工具名和调用 ID 此时须已知。厂商若分片返回这些字段，适配器先收集再发送 Start。
2. `BlockDelta`：携带字符串增量。参数尚未完整时不创建可序列化的 Msg。
3. `BlockEnd`：检查完整内容，标记块结束。失败不修改已有内容，可以继续补充增量。
4. `Finish()`：所有块结束、完整消息校验通过后返回独立消息，关闭聚合器。

不同块可交错输入，结果按 Start 顺序排列。已完成图片等通过 `AddBlock` 加入同一顺序。
文本/推理签名等非增量信息在 Start 时提供；当前不支持签名增量事件。
聚合器不提供网络重试去重、线程安全、取消状态或未完成消息快照；传输层负责事件顺序与去重，出错/取消时可丢弃聚合器。

## 所有权与复制

消息及块可变，`New`/`Add`/`BlocksOfType` 中的指针载荷是借用的。不要把可变消息同时交给多个 goroutine 修改。
`ToolUse` 构造函数复制参数字节；Aggregator 复制输入块。共享完整消息前使用：

```go
copy, err := original.Clone()
```

Clone 对块、嵌套结果和元数据进行 JSON 深拷贝；遇到无效内容显式返回错误，没有共享引用兜底。元数据改为 `map[string]json.RawMessage`，完整保留大整数及 JSON 结构，禁止任意 Go 对象。该实现优先正确性，尚未优化复制性能。


## 验证

在 goscope 目录：

```sh
go test -race ./...
go vet ./...
go run ./cmd/demo
```

保留两个 JSON 往返 benchmark 供当前版本测量。新实现包含验证逻辑，且样本已调整，不能与历史版本的数字直接比较；当前文章不引用旧基准数据。
