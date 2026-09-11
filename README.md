# goscope

用 Go 从零实现 AI Agent 的配套代码。当前完成消息层，还没有接入真实模型或天气服务。

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
