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
