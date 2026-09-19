package zapc

import (
	"fmt"
	"sync"
	"sync/atomic"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Watcher 由装配点注入的配置订阅能力：把 apply 挂到指定节的热更总线并返回
// 取消函数。签名与模板 config.Watch 结构化对齐——组件包不 import 模板
// config 包，节的选择留在持有配置树的装配点。
type Watcher func(apply func(Config) error) (cancel func())

type options struct{ watch Watcher }

// Option 构造选项。
type Option func(*options)

// WithWatch 注入配置节订阅：订阅即刻建立，节变更（含建立时的首调收敛）
// 自动走 kit.Apply 择路；取消函数并入 Close。与 AddComponent 的自动订阅
// 并存属双订阅，收敛语义下无害但多余——二选一。
func WithWatch(w Watcher) Option {
	return func(o *options) { o.watch = w }
}

// kitState 是 kit 的可变状态：实例、级别、配置与句柄回收。堆上共享，
// LoggerKit 以指针持有、保持值语义可拷贝。当前实例走原子指针——Current
// 是日志热路径，读取不进锁；mu 只串行化热更换实例与 cfg / sinkClose 记账
// （cur 与 cfg 必须成对更新，写侧同锁保证不出现交叉错配）。
type kitState struct {
	mu          sync.Mutex
	cur         atomic.Pointer[zap.Logger] // 当前实例（热路径原子读）
	cfg         Config                     // 当前生效配置（收敛判断基准）
	level       zap.AtomicLevel
	sinkClose   func() // 当前 sink 句柄回收
	watchCancel func() // WithWatch 注入的订阅取消（nil = 未注入）
}

// LoggerKit 是日志构建产物与热更状态机的句柄：其他想自建 zap 日志的组件
// 经 NewLogger 获得。自身不携带生命周期——全局安装等组件语义留在 Log。
// 零值不可用（Apply/Rebuild 报错、Current 返回 nil、Close 无操作）。
type LoggerKit struct{ st *kitState }

// NewLogger 按 cfg 构建日志实例并打包 LoggerKit。实例、动态级别与热更
// 状态全部收在 kit 内部——Current 始终返回当前实例，无外部事实源。
// WithWatch 注入订阅时，节变更自动驱动 Apply。
func NewLogger(cfg Config, opts ...Option) (LoggerKit, error) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	if err := cfg.Validate(); err != nil {
		return LoggerKit{}, err
	}
	parsed, err := zapcore.ParseLevel(cfg.Level)
	if err != nil {
		return LoggerKit{}, fmt.Errorf("zapc: invalid level %q", cfg.Level)
	}
	st := &kitState{cfg: cfg}
	st.level = zap.NewAtomicLevelAt(parsed)
	logger, sinkClose, err := buildLogger(cfg, &st.level)
	if err != nil {
		return LoggerKit{}, err
	}
	st.cur.Store(logger)
	st.sinkClose = sinkClose
	k := LoggerKit{st: st}

	// 订阅最后建立：取消函数并入 Close（先断订阅等在途回调，再关句柄）。
	if o.watch != nil {
		st.watchCancel = o.watch(k.Apply)
	}
	return k, nil
}

// Current 返回当前生效实例（原子读，热路径免锁）；热更换新后自动跟随。
func (k LoggerKit) Current() *zap.Logger {
	if k.st == nil {
		return nil
	}
	return k.st.cur.Load()
}

// Debug 记一条 Debug（经当前实例）。
func (k LoggerKit) Debug(msg string, fields ...zap.Field) {
	if l := k.Current(); l != nil {
		l.Debug(msg, fields...)
	}
}

// Info 记一条 Info（经当前实例）。
func (k LoggerKit) Info(msg string, fields ...zap.Field) {
	if l := k.Current(); l != nil {
		l.Info(msg, fields...)
	}
}

// Warn 记一条 Warn（经当前实例）。
func (k LoggerKit) Warn(msg string, fields ...zap.Field) {
	if l := k.Current(); l != nil {
		l.Warn(msg, fields...)
	}
}

// Error 记一条 Error（经当前实例）。
func (k LoggerKit) Error(msg string, fields ...zap.Field) {
	if l := k.Current(); l != nil {
		// 三索引切片强制 append 走新数组，不改写调用方 fields 的底层数组；
		// StackSkip(1) 跳过本封装帧，栈首帧即调用方代码行
		l.Error(msg, append(fields[:len(fields):len(fields)], zap.StackSkip("stack", 1))...)
	}
}

// DPanic 记一条 DPanic（经当前实例）。本组件非 Development 构建——只记
// 日志不 panic；需要"开发期 panic"语义时由调用方自行 Panic。
func (k LoggerKit) DPanic(msg string, fields ...zap.Field) {
	if l := k.Current(); l != nil {
		l.DPanic(msg, fields...)
	}
}

// Check 经当前实例的 Check：级别禁用时返回 nil（zap 原语义），调用方判空
// 后经 ce.Write(fields) 落盘。
func (k LoggerKit) Check(lvl zapcore.Level, msg string) *zapcore.CheckedEntry {
	if l := k.Current(); l != nil {
		return l.Check(lvl, msg)
	}
	return nil
}

// Apply 智能热更入口：收敛（相同值无操作）+ 择路（仅级别即时，其余重建）。
func (k LoggerKit) Apply(cfg Config) error {
	if k.st == nil {
		return fmt.Errorf("zapc: zero LoggerKit")
	}
	return k.st.apply(cfg)
}

// Rebuild 强制重建实例（跳过收敛判断）：换实例、同步 zap 全局、换新句柄。
func (k LoggerKit) Rebuild(cfg Config) error {
	if k.st == nil {
		return fmt.Errorf("zapc: zero LoggerKit")
	}
	return k.st.rebuild(cfg)
}

// Close 取消配置订阅（如经 WithWatch 注入）并释放 sink 句柄；幂等
// （cancel 自带 once，文件重复关闭吞错误）。
func (k LoggerKit) Close() {
	if k.st == nil {
		return
	}
	if k.st.watchCancel != nil {
		k.st.watchCancel()
	}
	k.st.closeSinks()
}

// rebuild 重建实例：换实例与句柄、同步级别与配置记账，锁外刷旧盘关旧
// 句柄（即刻回收，热更不限次不漏 fd）并同步 zap 全局。
func (s *kitState) rebuild(c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	p, err := zapcore.ParseLevel(c.Level)
	if err != nil {
		return fmt.Errorf("zapc: invalid level %q", c.Level)
	}
	next, nextClose, err := buildLogger(c, &s.level)
	if err != nil {
		return err
	}
	s.mu.Lock()
	old, oldClose := s.cur.Load(), s.sinkClose
	s.cur.Store(next)
	s.sinkClose = nextClose
	s.level.SetLevel(p)
	s.cfg = c
	s.mu.Unlock()
	_ = old.Sync() // 尽力刷盘：旧实例可能仍有在途写入
	oldClose()
	zap.ReplaceGlobals(next)
	return nil
}

// apply 收敛 + 择路：仅 Level 变更走 AtomicLevel.SetLevel（实例不换），
// 其余任一字段变更走重建（自带锁）。
func (s *kitState) apply(c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	same, smart := c == s.cfg, levelOnly(s.cfg, c)
	s.mu.Unlock()
	switch {
	case same:
		return nil
	case smart:
		p, err := zapcore.ParseLevel(c.Level)
		if err != nil {
			return fmt.Errorf("zapc: invalid level %q", c.Level)
		}
		s.mu.Lock()
		s.level.SetLevel(p)
		s.cfg = c
		s.mu.Unlock()
		return nil
	default:
		return s.rebuild(c)
	}
}

// closeSinks 在锁外执行当前句柄回收。
func (s *kitState) closeSinks() {
	s.mu.Lock()
	closeFn := s.sinkClose
	s.mu.Unlock()
	closeFn()
}
