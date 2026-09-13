package tool

// Progress 描述一次工具调用的调度与完成。未知工具也有 started/failed。
// started 在执行前发出，不表示函数已经进入；取消可能阻止实际执行。
type Progress struct {
	CallID   string
	Name     string
	Finished bool
	Status   string
}

// 原始观察者 panic 被隔离；Agent 的 Hook 自行记录错误计数。
func reportProgress(observer func(Progress), p Progress) {
	if observer == nil {
		return
	}
	defer func() { _ = recover() }()
	observer(p)
}
