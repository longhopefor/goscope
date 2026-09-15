# goscope

用 Go 从零实现 AI Agent 的配套代码。当前完成消息层、Model 接口及 Chat Completions HTTP 适配；提供本地模拟验证和可选真实调用入口，尚未实测真实模型或接入天气服务。

## 环境与运行

需要 Go 1.24 或更高版本，当前仅使用标准库。

```sh
git clone https://github.com/longhopefor/goscope.git
cd goscope
go test -race ./...
go vet ./...
go run ./cmd/demo
```

Demo 演示流式工具参数聚合、JSON 往返、图文工具结果、调用关联和深拷贝隔离。天气为演示数据。

## 目录

- `msg/`：受控内容块、完整消息校验、流式聚合器及测试。
- `cmd/demo/`：可运行示例。

设计边界和 API 说明见 [消息包文档](msg/README.md)。

## 第 2 篇：Model 与 Formatter

```sh
go run ./cmd/model-demo
go run ./cmd/model-demo -stream
```

默认运行本地模拟，不需要密钥。真实调用和能力边界见 [Model 文档](model/README.md)。

## 第 3 篇：Tool

```sh
go run ./cmd/tool-demo
```

工具定义、受限 Schema 生成、参数校验、只读注册表和串行执行已实现。Demo 在本机执行加法，调用消息为手动构造。用法与限制见 [Tool 文档](tool/README.md)。

## 第 4 篇：ReAct

```sh
go run ./cmd/agent-demo
```

脚本模型与本地工具完成两轮闭环，包含模型调用次数上限、停止原因和取消传播。使用方式及可选真实模型入口见 [Agent 文档](agent/README.md)。

## Agent 执行进度

`agent-demo` 现在实时打印模型和每个工具的开始、结束与运行终态。应用可通过 `RunWithRequest` 的 Hook 接收只读事件；原 Run 接口继续可用。详见 [进度接口说明](agent/README.md#执行进度)。

### 取消与部分执行示例

运行 `go run ./cmd/cancel-demo`：A 成功，B 写入临时文件后超时并标为 unknown，C 保持 not_started。示例结束清理临时文件。支持运行、模型、单次工具三级合作式超时；`Result.ToolBatches` 保留逐项状态，旧工具执行接口继续兼容。当前记录仅在内存中，不支持自动恢复或安全自动重试。

### 统一 Runner

`agent.NewRunner(strategy)` 接受实现 `agent.Agent` 的执行策略，统一运行 ID、总超时、事件和错误收尾。现有 ReAct.Run 仍可用。执行 `go run ./cmd/runner-demo` 对照两种策略的运行过程；示例无需 API key。
