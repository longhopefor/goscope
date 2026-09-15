# 第 3 篇：Tool

`tool.New[T]` 把 `func(context.Context, T) ([]msg.Block, error)` 包装成工具。工具定义复用 `model.Tool`，调用输入使用 `json.RawMessage`，输出使用 msg 的文字/图片/音频块。

```go
type Args struct {
    A int64 `json:"a" tool:"required,min=-1000000,max=1000000" description:"第一个整数"`
    B int64 `json:"b" tool:"required,min=-1000000,max=1000000" description:"第二个整数"`
}
add, err := tool.New("add", "整数加法", func(ctx context.Context, a Args) ([]msg.Block, error) {
    if err := ctx.Err(); err != nil { return nil, err }
    return []msg.Block{msg.Text(fmt.Sprint(a.A + a.B))}, nil
})
// 处理 err 后创建只读注册表。
registry, err := tool.NewRegistry(add)
// registry.Definitions() 可直接传给 model.Request.Tools。
```

## 参数与 Schema

Schema 在构造工具时由反射生成，使用同一字段树执行运行时校验。它是受限 JSON Schema，不是通用 Schema 引擎。

- T 必须是结构体。支持 string、bool、有符号/无符号整数、float32/64、嵌套结构体、切片；拒绝指针、map、interface、数组、[]byte、嵌入字段、递归类型、自定义 JSON/Text 解码类型。
- 每个参与字段必须导出，并有显式 json 名称。`json:"-"` 忽略字段；仅允许 omitempty 选项，它不改变必填规则。
- `tool:"required"` 按键存在判断，0、false 和空字符串都是合法值。所有显式 null 都拒绝；缺省可选字段保持 Go 零值。
- `tool:"enum=a|b"` 仅支持字符串枚举；`min`/`max` 为包含边界的数值约束；未知或重复标签、反向范围在构造时拒绝。
- `description` 提供字段说明。未知字段、大小写不符的字段、重复 JSON 键、尾随文档拒绝。
- 数值先保留 json.Number，再按目标 Go 类型验证，避免 float64 中转丢大整数精度。整数按 Go JSON 解码词法，不接受 1.0/1e0；这比通用 JSON Schema integer 语义严格。
- 入参最多 1 MiB，嵌套深度最多 32，数值文本最多 128 字符、指数绝对值不超过 1000。Schema 标签中的数值长度另限制为 64。
- 范围约束由标签指定，目标 Go 类型的溢出在运行时额外检查。未提供范围标签时，生成的 Schema 不保证涵盖所有 Go 数值位宽限制。
- New[T] 的函数类型由编译器检查，字段存在、范围等由运行时检查。业务语义仍由工具函数负责。

## 注册与执行

`NewRegistry(tools...)` 拒绝重复名字，保存定义副本；构造后没有动态 Register。Definitions 返回副本。Registry 的只读查找可以并发使用，但 Tool 函数及自定义实现必须自己保证并发安全。自定义 Tool 的定义应保持不变、Call 应自行校验参数；Registry 只检查 Schema 顶层 object，不替自定义工具执行完整 Schema 校验。

`Execute(ctx, assistantMsg)` 先复制并校验整条消息，再按顺序执行其中的 ToolUse；无调用、非 assistant 消息和损坏消息直接返回 error，不执行函数。

- 未知工具、无效参数、普通业务错误、panic、无效输出 → `ToolResultBlock.IsError=true`，ID 对应原调用；继续后续工具。
- 普通业务错误和 panic 内容不直接暴露给模型。包装器错误通过 code/message 表达；自定义 `*tool.Error` 是有意公开的信息，调用方不要放敏感内容。
- context 取消/超时 → 立即停止派发，向上返回 error，不返回完整结果消息。
- 函数必须配合 context；执行器不会另起 goroutine 假装可以强制终止。
- 取消时前面的工具可能已经执行成功。当前没有部分进度持久化、幂等或回滚，不能因为整批返回错误就盲目重跑。
- 输出只允许 text/image/audio，深拷贝后返回，不与函数提供的数据共享可变引用。
- 当前模型适配器只接受文本工具结果：工具层支持图片不等于模型适配器支持图片结果。

本层不调用模型、不进行 ReAct 循环、不并行执行、不重试。下一篇接 Agent 循环。

## 验证

仓库根目录：

```sh
go run ./cmd/tool-demo
go test -race ./...
go vet ./...
```

Demo 手动构造三次调用，执行真正的本地加法函数，并验证调用与结果关系，不需要密钥。测试还覆盖合法零值、缺失/未知字段、嵌套约束、大整数、取消后不继续派发、panic 转换、输出隔离，以及结果经现有 Formatter 编码。

## 单项进度观察

`ExecuteWithObserver(ctx, request, func(Progress))` 在每个调用调度前和完成后同步通知。原 Execute 保持兼容。Progress 仅包含 CallID、Name、Finished 和 Status，无法修改工具参数或结果。Status 为 started、succeeded、failed 或 canceled；未知工具也发出 started/failed。

观察者 panic 被隔离并继续执行；直接使用此底层 API 不记录观察者异常。需要失败计数时使用 Agent 的 Hook。观察者必须及时返回，context 不会强制终止阻塞回调。开始通知之后会再次检查取消，观察者取消运行可以阻止函数执行；完成通知之后也检查取消，避免启动下一项。取消不回滚已经发生的副作用。

### 取消后的逐项结果

需要保留部分结果时使用 `ExecuteBatch(ctx, request, tool.BatchOptions{Timeout: time.Second})`。返回的 `BatchResult.Calls` 按请求顺序记录 `succeeded`、`failed`、`unknown`、`not_started`；`Message` 只包含有确定返回结果的项，可能为 nil。取消时仍应读取 batch，并用 `errors.Is` 检查 error。

`AttemptID` 表示进入 Tool.Call 边界，不证明业务函数执行，更不是持久化幂等键。`unknown` 不可直接自动重试，`failed` 也不证明没有副作用。旧 Execute/ExecuteWithObserver 保持取消返回 nil,error。超时要求工具响应 context，不能强制终止业务代码。
