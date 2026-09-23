package zapc

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Watcher 由装配点注入的配置订阅能力：把 apply 挂到指定节的热更总线并返回
// 取消函数。签名与模板 config.Watch 结构化对齐——组件包不 import 模板
// config 包，节的选择留在持有配置树的装配点。
type Watcher func(apply func(Config) error) (cancel func())

type options struct {
	watch     Watcher
	onSwap    func(*zap.Logger)                 // 实例换新回调（初始构建与每次热更重建都触发）
	extra     []zapcore.Core                    // 旁路 core（WithCore 注入；nil 项忽略）
	ctxAttrs  func(context.Context) []slog.Attr // ctx 属性提取（WithCtxAttrs 注入；nil = 不注入）
	encMutate func(*zapcore.EncoderConfig)      // 编码器定制（WithEncoderConfig 注入；nil = 基准）
}

// Option 构造选项。
type Option func(*options)

// WithCore 注入旁路 core：与组件自建 core 经 NewTee 并联、共享构建路径
// ——初始构建与每次热更重建都自动带上（core 是构建参数而非一次性注入，
// 不存在"重建后攥着旧 tee"的脱落问题）。供跨组件组合：如 otelc 的
// OTLP 日志导出 core 由装配点经本选项送入。core 的启停与 flush 生命周期
// 归提供方组件；zapc 只负责写入（Sync 经 tee 自然传播）。传 nil 忽略。
func WithCore(core zapcore.Core) Option {
	return func(o *options) {
		if core != nil {
			o.extra = append(o.extra, core)
		}
	}
}

// WithWatch 注入配置节订阅：订阅即刻建立，节变更（含建立时的首调收敛）
// 自动走 kit.Apply 择路；取消函数并入 Close。与 AddComponent 的自动订阅
// 并存属双订阅，收敛语义下无害但多余——二选一。
func WithWatch(w Watcher) Option {
	return func(o *options) { o.watch = w }
}

// WithCtxAttrs 注入 ctx 属性提取器：经稳定桥（observ 默认日志器）的
// 每次调用在适配层内部从 ctx 追加属性（链路注入 trace_id/span_id/
// request_id 等，提取函数由装配点从 otelc.CtxLogAttrs 传入）。注入在
// zaplog 适配层完成——调用面无装饰层，caller 定位不随封装漂移。只影响
// 稳定桥，不影响 zap.L() 直调与 Current 调用面。传 nil 忽略。
func WithCtxAttrs(fn func(context.Context) []slog.Attr) Option {
	return func(o *options) { o.ctxAttrs = fn }
}

// WithEncoderConfig 注入编码器定制：以基准 EncoderConfig 为入参的就地
// 修改函数（如请求日志关闭 caller：置空 CallerKey）。构建期属性，初始
// 构建与每次热更重建都生效；不改 yaml 配置面。传 nil 忽略。
func WithEncoderConfig(mutate func(*zapcore.EncoderConfig)) Option {
	return func(o *options) { o.encMutate = mutate }
}

// WithOnSwap 注入实例换新回调：初始构建与每次热更重建后以新实例调用一次
// （锁外执行）。供外部绑定跟随实例的旁路设施；不跟随热更的绑定会在重建
// 后攥着已关闭的旧实例。回调收到原始实例（skip 0），调用面深度由回调方
// 自行校准。（observ 默认日志器的接管不走此回调——稳定桥包装 kit，
// 与实例换新正交，见 Log.New。）
func WithOnSwap(fn func(*zap.Logger)) Option {
	return func(o *options) { o.onSwap = fn }
}

// kitState 是 kit 的可变状态：实例、级别、配置与句柄回收。堆上共享，
// LoggerKit 以指针持有、保持值语义可拷贝。实例走原子指针——Current 是
// 日志热路径，读取不进锁；mu 只串行化热更换实例与 cfg / sinkClose 记账
// （cur 与 cfg 必须成对更新，写侧同锁保证不出现交叉错配）。双实例分工：
// cur 为原始实例（skip 0，供 zap.L() / Current 直调），curSkip1 跳过一层
// 封装帧（kit 调用面专用，caller 定位到用户行）。
type kitState struct {
	mu          sync.Mutex
	cur         atomic.Pointer[zap.Logger] // 原始实例（热路径原子读）
	curSkip1    atomic.Pointer[zap.Logger] // 跳一层封装帧的实例（kit 调用面专用）
	cfg         Config                     // 当前生效配置（收敛判断基准）
	level       zap.AtomicLevel
	extra       []zapcore.Core               // WithCore 注入的旁路 core（每次构建都并联）
	encMutate   func(*zapcore.EncoderConfig) // WithEncoderConfig 注入（构造期冻结，每次构建生效）
	sinkClose   func()                       // 当前 sink 句柄回收
	watchCancel func()                       // WithWatch 注入的订阅取消（nil = 未注入）
	onSwap      func(*zap.Logger)            // WithOnSwap 注入的换新回调（nil = 未注入）
}

// LoggerKit 是日志构建产物与热更状态机的句柄：其他想自建 zap 日志的组件
// 经 NewLogger 获得。自身不携带 Start/Stop 生命周期（启停装全局留在
// Log）；但重建仍会同步 zap 全局（见 Rebuild）——独立 kit 的 Apply /
// Rebuild 同样 ReplaceGlobals。零值不可用（Apply/Rebuild 报错、Current
// 返回 nil、Close 无操作）。
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
	st.extra = o.extra
	st.encMutate = o.encMutate
	logger, sinkClose, err := buildLogger(cfg, &st.level, st.extra, st.encMutate)
	if err != nil {
		return LoggerKit{}, err
	}
	st.cur.Store(logger)
	st.curSkip1.Store(logger.WithOptions(zap.AddCallerSkip(1)))
	st.sinkClose = sinkClose
	st.onSwap = o.onSwap
	k := LoggerKit{st: st}

	// 换新回调先于订阅建立：绑定方拿到的实例与热更路径完全同源。
	if o.onSwap != nil {
		o.onSwap(logger)
	}

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

// CurrentSkip1 返回当前生效实例（原子读，热路径免锁）；热更换新后自动跟随。
func (k LoggerKit) CurrentSkip1() *zap.Logger {
	if k.st == nil {
		return nil
	}
	return k.st.curSkip1.Load()
}

// Debug 记一条 Debug（经当前实例）。
func (k LoggerKit) Debug(msg string, fields ...zap.Field) {
	if l := k.CurrentSkip1(); l != nil {
		l.Debug(msg, fields...)
	}
}

// Info 记一条 Info（经当前实例）。
func (k LoggerKit) Info(msg string, fields ...zap.Field) {
	if l := k.CurrentSkip1(); l != nil {
		l.Info(msg, fields...)
	}
}

// Warn 记一条 Warn（经当前实例）。
func (k LoggerKit) Warn(msg string, fields ...zap.Field) {
	if l := k.CurrentSkip1(); l != nil {
		l.Warn(msg, fields...)
	}
}

// Error 记一条 Error（经当前实例）。
func (k LoggerKit) Error(msg string, fields ...zap.Field) {
	if l := k.CurrentSkip1(); l != nil {
		// 三索引切片强制 append 走新数组，不改写调用方 fields 的底层数组；
		l.Error(msg, append(fields[:len(fields):len(fields)], zap.StackSkip("stack", 1))...)
	}
}

// DPanic 记一条 DPanic（经当前实例）。本组件非 Development 构建——只记
// 日志不 panic；需要"开发期 panic"语义时由调用方自行 Panic。
func (k LoggerKit) DPanic(msg string, fields ...zap.Field) {
	if l := k.CurrentSkip1(); l != nil {
		l.DPanic(msg, fields...)
	}
}

// Check 经当前实例的 Check：级别禁用时返回 nil（zap 原语义），调用方判空
// 后经 ce.Write(fields) 落盘。
func (k LoggerKit) Check(lvl zapcore.Level, msg string) *zapcore.CheckedEntry {
	if l := k.CurrentSkip1(); l != nil {
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
	next, nextClose, err := buildLogger(c, &s.level, s.extra, s.encMutate)
	if err != nil {
		return err
	}
	s.mu.Lock()
	old, oldClose := s.cur.Load(), s.sinkClose
	s.cur.Store(next)
	s.curSkip1.Store(next.WithOptions(zap.AddCallerSkip(1)))
	s.sinkClose = nextClose
	s.level.SetLevel(p)
	s.cfg = c
	s.mu.Unlock()
	_ = old.Sync() // 尽力刷盘：旧实例可能仍有在途写入
	oldClose()
	zap.ReplaceGlobals(next)
	if s.onSwap != nil {
		s.onSwap(next)
	}
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
