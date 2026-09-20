package zapc

import (
	"fmt"
	"go_template/pkg/constant"
	"os"
	"path/filepath"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

// buildLogger 组装实例：文件输出（Format 编码 + lumberjack 轮转）与控制台
// 输出（固定人类可读编码）各自成 core，与 WithCore 注入的旁路 core 一起
// 经 NewTee 并联、共享同一动态级别。返回的关闭函数在热更换新与失败路径
// 上就地回收句柄（旁路 core 的生命周期归提供方，不经此回收）。
func buildLogger(cfg Config, lvl zapcore.LevelEnabler, extra []zapcore.Core) (*zap.Logger, func(), error) {
	var cores []zapcore.Core
	var closers []func()
	closeAll := func() {
		for _, c := range closers {
			c()
		}
	}
	if cfg.Path != "" {
		// 建目录 + 探针打开：目录就地创建；lumberjack 惰性开文件，坏路径会
		// 拖到首次写才暴露，先开一次把打不开提前到构造期（fail-fast）。
		if err := os.MkdirAll(filepath.Dir(cfg.Path), 0o755); err != nil {
			return nil, nil, fmt.Errorf("zapc: create log dir %q: %w", filepath.Dir(cfg.Path), err)
		}
		f, err := os.OpenFile(filepath.Clean(cfg.Path), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o666)
		if err != nil {
			return nil, nil, fmt.Errorf("zapc: open output %q: %w", cfg.Path, err)
		}
		f.Close()
		lj := &lumberjack.Logger{
			Filename:   cfg.Path,
			MaxSize:    cfg.MaxSize,
			MaxAge:     cfg.MaxAge,
			MaxBackups: cfg.MaxBackups,
			Compress:   cfg.Compress,
			LocalTime:  true, // 轮转文件名取本地时区，与控制台时间戳一致
		}
		cores = append(cores, zapcore.NewCore(newEncoder(cfg.Format), zapcore.AddSync(lj), lvl))
		closers = append(closers, func() { _ = lj.Close() })
	}
	if cfg.LogToConsole {
		cores = append(cores, zapcore.NewCore(newConsoleEncoder(), zapcore.Lock(os.Stdout), lvl))
	}
	cores = append(cores, extra...)
	if len(cores) == 0 { // Validate 已挡，防御性兜底
		return nil, nil, fmt.Errorf("zapc: no output destination")
	}
	return zap.New(zapcore.NewTee(cores...), zap.AddCaller()), closeAll, nil
}

// encoderConfig 人类可读基准：大写级别。控制台加色，文件保持无色（避免
// ANSI 转义污染落盘内容）。
func encoderConfig(colored bool) zapcore.EncoderConfig {
	enc := zapcore.EncoderConfig{
		TimeKey:        "time",
		LevelKey:       "level",
		NameKey:        "logger",
		CallerKey:      "caller",
		MessageKey:     "msg",
		StacktraceKey:  "stack",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeTime:     zapcore.TimeEncoderOfLayout(constant.RFC3339Milli),
		EncodeDuration: zapcore.MillisDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
	}
	if colored {
		enc.EncodeLevel = zapcore.CapitalColorLevelEncoder
	} else {
		enc.EncodeLevel = zapcore.CapitalLevelEncoder
	}
	return enc
}

// newEncoder 构造文件 encoder：console 人类可读 / json 机器可解析。
func newEncoder(format string) zapcore.Encoder {
	if format == "json" {
		return zapcore.NewJSONEncoder(encoderConfig(false))
	}
	return zapcore.NewConsoleEncoder(encoderConfig(false))
}

// newConsoleEncoder 控制台固定人类可读并加色，不随 Format 走 json。
func newConsoleEncoder() zapcore.Encoder {
	return zapcore.NewConsoleEncoder(encoderConfig(true))
}
