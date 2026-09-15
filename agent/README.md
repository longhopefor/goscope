# ReAct

非流式模型 → 串行工具 → 结果回填 → 再调用模型。没有工具调用的完整 assistant 消息视为完成，不要求模型输出或暴露推理文本。

```go
a, err := agent.New(m, registry, 4) // m: model.Model
if err != nil { return err }
result, err := a.Run(ctx, history)
```

- 使用 New 构造；registry 必填，无工具场景使用空注册表。maxSteps 必须大于零。
- Run 的输入必须非空、有效，且不存在未配对的工具调用。
- Steps 为已启动的 Generate 次数，包含失败的请求。MaxSteps 只限制模型次数，不限制单轮工具数量或整体耗时。
- 最后一轮如果要求工具，仍执行并保存结果，随后返回 ErrMaxSteps；不会再请求模型总结。
- Result 即使错误也非空。StopReason 为 completed、max_steps、canceled 或 failed。仅 completed 有 Final；Final 与 History 独立。
- 模型错误（即使同时返回响应）、nil 响应、错误角色、非法消息、重复调用 ID 均终止；错误原因可通过 errors.Is 判断。Model 应只在完整响应成功时返回 nil error，Agent 不解释厂商 FinishReason。
- 普通工具失败形成 IsError 结果，下一轮模型可以看到；取消或超时终止运行，不自动重试。
- 取消时 History 可能含未配对的 assistant 工具调用，并且工具可能已有副作用。这是诊断快照，不是恢复检查点，不能直接重放整批。
- Run 历史是局部变量，输入、模型请求和响应在边界复制。调用方仍不能并发修改输入；并发 Run 依赖 Model 与工具函数自身并发安全。不提供持久会话。
- context 需要下游配合，无法强制终止不响应取消的函数。没有流式 Agent、自动重试、并行工具、持久化或 Hooks。

## 本地验证

```sh
go run ./cmd/agent-demo
go test -race ./...
go vet ./...
```

默认脚本模型根据历史返回 ToolUse，再读取本地函数结果生成最终文本；没有真实 LLM。

```sh
go run ./cmd/agent-demo -max-steps 1
```

预期 max_steps、一次工具执行、无最终回答、非零退出状态。

## 可选真实服务

设置 OPENAI_API_KEY、OPENAI_MODEL，可选 OPENAI_BASE_URL（含 /v1），然后显式运行：

```sh
go run ./cmd/agent-demo -real
```

使用现有 Chat Completions 适配器和同一个 Run 接口，运行超时 30 秒。模型可能不选择工具，因此须检查实际结果；此入口本次未实测。不在命令行或仓库保存密钥。

## 执行进度

原 `Run(ctx, history)` 保持兼容。需要进度时，使用每次运行独立的请求：

```go
result, err := a.RunWithRequest(ctx, agent.RunRequest{
    Messages: history,
    Hook: func(e agent.Event) error {
        fmt.Printf("%s #%d %s step=%d tool=%s status=%s stop=%s\n",
            e.RunID, e.Sequence, e.Type, e.Step, e.ToolName, e.Status, e.StopReason)
        return nil
    },
})
```

- 顺序为 run_started → model_started/model_finished → 可选的 tool_started/tool_finished → 后续模型轮次 → run_finished。每项工具均有通知，未知工具也会产生 started/failed。
- 在正常返回与错误返回路径中，run_finished 恰好一次，StopReason 与 Result 一致；无效输入、预先取消也有运行开始与结束通知。
- Event 的 RunID 对应 Result.RunID；Sequence 从 1 连续递增，Time 是发出时间；Step 为模型尝试的轮次。RunFinished.Step 为实际启动的 Generate 次数，与 Result.Steps 一致。
- started 在实际执行前发出。Hook 如果通过捕获的 cancel 函数取消运行，该尝试会产生 canceled 结束事件，实际函数可以未执行；因此取消时最后一个模型尝试编号可能比 Result.Steps 大 1。
- model_finished 的 succeeded 表示响应已通过 Agent 消息验证；工具失败不影响模型调用本身的成功状态。tool_finished 的 failed 表示失败工具结果，canceled 表示取消；取消不证明工具没有副作用。
- Hook 同步、串行、在执行 goroutine 内调用。慢 Hook 会阻塞进度；不响应返回的 Hook 无法被 context 强制终止。不额外创建队列或 goroutine。
- Hook 返回错误或 panic 都只增加 Result.HookFailures，不改变主任务结果，后续通知继续发送。包括最终通知的失败也会计数。不保留原始错误文本，避免进度里泄露敏感内容。
- Event 仅有值字段，不携带消息、参数、结果或错误对象，修改事件不会影响历史。Hook 捕获的其他共享对象不在隔离保证范围内；并发 Run 的 Hook 需要自行保护共享数据。
- run_finished 是结果确定后的通知，在这个 Hook 中取消 context 不会追溯修改已确定的终态。
- Demo 默认显示这些事件。这是步骤进度，不是模型逐字流式输出；没有百分比、Hooks 持久化或断点恢复。

### 分层超时与部分执行

`RunRequest.Timeouts` 支持 `agent.Timeouts{Run: time.Minute, Model: 20*time.Second, Tool: 5*time.Second}`。0 继承父时限，负值拒绝，更早的父 deadline 优先。模型和工具需合作处理 context，Hook 不应阻塞。

失败时读取 `Result.ToolBatches` 获取逐项执行状态；History 保留已确认的工具输出，但可能仍有未解决调用，不能直接继续 Run。DeadlineExceeded 沿用 Canceled 停止原因，具体原因通过 errors.Is 判断。本次未加入自动重试或持久恢复。

无需 API key 的验证：`go run ./cmd/cancel-demo`。

### Runner：统一运行入口

```go
runner, err := agent.NewRunner(reactAgent)
if err != nil { return err }
result, err := runner.Run(ctx, agent.RunRequest{Messages: input, Hook: hook})
```

`agent.Agent` 实现 `Execute(context.Context, agent.ExecutionRequest) (*agent.Result, error)`，无需负责生成 RunID 或发送运行开始/结束事件。ExecutionRequest 提供消息、ModelTimeout、ToolTimeout 和同步 Emit。模型/工具事件由策略发出，运行级事件及元数据由 Runner 管理。ReAct.Run/RunWithRequest 已委托同一个 Runner。

Runner 总是返回非 nil Result；策略 nil,error 会得到失败结果，nil,nil 为契约错误；取消时保留策略已返回的部分结果，errors.Is 可查原因。同步 panic 返回通用错误，但不能取回策略未返回的局部结果，也不能捕获其他 goroutine 的 panic。策略返回后不得继续修改结果或留下运行中的工作。

同次运行 Hook 串行调用，不要在 Hook 中递归 Emit 或等待依赖当前 Hook 返回的工作。跨运行共用 Hook 的共享状态需自行同步。Runner 可复用，但 Agent 和其依赖也必须支持并发。当前无后台运行、持久恢复和强制超时。

运行 `go run ./cmd/runner-demo` 对照直接返回策略与 ReAct；再运行 `go run ./cmd/cancel-demo` 验证逐项取消结果。

### 端到端流式

`Runner.Stream(ctx, RunRequest)` 返回单消费者 Stream，逐项 `Recv()`，读到结束后 `Wait()` 取得完整 Result。提前停止读取务必 `Close()`；队列满时不要直接 Wait。内容与 Hook 进度分开，工具只在完整模型流校验成功后执行。

示例：`go run ./cmd/stream-demo`。具体所有权、超时、关闭及 OpenAI 参数缓冲行为见 [流式调用文档](STREAMING.md)。

### 会话运行

`Runner.RunSession` / `StreamSession` 接收 Store、Namespace + SessionID 和新增 user 消息，自动加载历史并在运行结束前按旧版本保存。返回值检查 SessionSaved 和 error；写入冲突不自动重跑。失败运行保存为 Blocked，后续直接续跑被拒绝。

使用方式、SQLite 构建依赖及“版本检查不等于工具执行去重”的边界见 [会话与版本化存储](../session/README.md)。
