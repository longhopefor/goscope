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
