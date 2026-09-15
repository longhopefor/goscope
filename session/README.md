# 会话管理与版本化存储

Session 是跨 Run 的对话快照。Key 由 Namespace + ID 组成；RunID 每轮独立。Namespace 只是存储隔离键，调用方仍须负责认证与授权，不能直接相信客户端传入的 Namespace。

## 数据与接口

`Snapshot` 保存 FormatVersion、Key、Version、State、History 和最后一轮的 LastRunID/StopReason/ToolBatches。ToolBatches 只保存最后一轮诊断，不是逐动作持久账本。

`Store.Load(ctx,key)` 返回独立副本；不存在返回 ErrNotFound。`Store.Save(ctx,snapshot)` 的 snapshot.Version 是预期旧版本：0 创建、N 条件更新为 N+1。成功返回新副本，不修改入参；冲突返回 ErrConflict，不覆盖、不自动合并。缺失记录上更新非零版本也算冲突。

FormatVersion=1 是 JSON 格式版本，与每次保存递增的 Version 不同。数据库另有 schema 版本表；未知格式/数据库版本拒绝使用，不尝试降级覆盖。

## 后端

- `session.NewMemory()`：线程安全内存实现，深复制输入输出，不持久化。
- `session/sqlite.Open(path)`：SQLite 文件，调用方负责 Close。WAL、FULL synchronous、5 秒 busy timeout；每个 Store 一条连接，通过主键与 SQL 条件更新保证原子版本检查。两个 Store 连接同一文件也参与相同条件更新。

SQLite 固定使用 github.com/mattn/go-sqlite3 v1.14.32，要求 CGO_ENABLED=1 和 C 编译器。只有使用该后端的程序需要这一依赖；跨平台构建要准备对应 C 工具链。数据库目录、文件权限与备份由应用管理；不要把数据库放进 Git。

## 创建及运行

函数内示例（省略包 import）：

```go
store, err := sqlite.Open("sessions.db")
if err != nil { return err }
defer store.Close()
key := session.Key{Namespace: "my-app", ID: "chat-1"}
_, err = store.Save(ctx, session.New(key)) // 仅创建时执行；已有记录会冲突
if err != nil { return err }
result, err := runner.RunSession(ctx,
    agent.SessionRequest{Store: store, Key: key},
    agent.RunRequest{Messages: []*msg.Msg{
        msg.NewText("user", msg.RoleUser, "你好"),
    }},
)
```

每次只传新增 user 消息，历史由 Runner 加载。系统提示可以写入初始 Snapshot.History 后保存。会话必须先创建；不会把任意加载错误当成不存在而自动创建。

流式对应 `runner.StreamSession(ctx,binding,req)`；仍然遵守读完或 Close 后再 Wait。原 Run/Stream 不传 Session 时行为不变。

自定义 Agent 若返回 History，必须保留加载历史与本轮输入的完整前缀；只返回 Final 的简单策略会在保存时组成“输入历史 + Final”。策略返回无效或改写过的历史时拒绝保存。

## 保存与错误

Runner 在执行结束后、RunFinished 通知之前尝试保存。Result 提供 SessionID、SessionVersion、SessionSaved。保存成功才会 SessionSaved=true，版本为提交后的版本。保存失败时保留原执行结果及错误链，不会自动重跑。执行成功但保存失败，Final 仍可供调用方查看，StopReason=Failed，返回 error；不要只看 Final 就报告整轮成功。

运行 context 被取消后，使用 context.WithoutCancel 保留 context 值，再建立独立保存时限。SessionRequest.SaveTimeout 默认 3 秒，负值拒绝；总运行耗时可能多出这段收尾时间。自定义 Store 必须响应 context。保存超时的 error 不等于数据库一定没写入，调用方应重新读取核对，不能盲目重复整轮。

模型/工具/策略执行失败后的快照保守标为 Blocked，即使没有未解决工具调用也需要人工或应用明确处理。含未解决调用的历史不能作为 Ready 保存。Blocked 会话下一轮在调用 Agent 前返回 ErrBlocked。本轮不提供“清空状态就自动重跑”的接口。

## 不提供的保证

版本检查防止历史覆盖，**不提供运行执行权**。两个并发 Run 都可能先执行工具，最后只有一个能保存；失败者的工具副作用不会回滚。应用当前应避免同会话并发运行带副作用的工具；以后由执行账本和执行权机制处理。

持久化发生在本轮结束时。进程在工具产生副作用后、保存前崩溃，旧会话可能仍是 Ready，不能据此推断工具没有执行。Snapshot 不是 checkpoint，不承诺 exactly-once、安全自动重试、断点恢复或审批恢复。

最后一轮 ToolBatches 是诊断快照，不是不可变审计日志。没有自动截断历史、产物存储、列表分页、删除/归档或多 worker 协调。

## 两次进程运行验证

```bash
go run ./cmd/session-demo -db /tmp/goscope-session-demo.db -message hello
go run ./cmd/session-demo -db /tmp/goscope-session-demo.db -message continue
```

首次使用一个不存在的新数据库时，输出为：

```text
loaded version=1 messages=0
saved=true version=2 answer=turn=1 first="hello"
loaded version=2 messages=2
saved=true version=3 answer=turn=2 first="hello"
```

模型是读取实际消息历史的脚本，不需要 API key。这验证进程退出后历史仍在，不验证真实模型记忆能力。

测试包含内存/SQLite 同合同、输入输出隔离、命名空间隔离、创建冲突、两个版本写者竞争、SQLite 关闭重开和独立连接竞争、取消保存、未知格式拒绝；Agent 测试包括保存失败不重跑、保存后终态、限时收尾、Blocked 拒绝和流式会话保存。
