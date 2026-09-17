// Package cmd 是命令层：参数解析、子命令、进程退出码。
package cmd

import (
	"fmt"
	"os"

	"go_template/internal/app"

	"github.com/spf13/cobra"
)

var (
	configPath string // --config：基础配置文件路径
	envFlag    string // --env：运行环境（缺省读 APP_ENV；与 APP_ENV 同为多环境文件的唯一选择输入）
	logLevel   string // --log-level：静态覆盖 log.level，置于合并栈顶
)

// rootCmd 即 run 命令（root 默认，无参数即执行）：完整启动时序，阻塞至信号。
var rootCmd = &cobra.Command{
	Use:   "run",
	Short: "启动长驻服务（默认命令，无参数即执行）",
	RunE: func(cmd *cobra.Command, args []string) error {
		return app.Run(configPath, envFlag, logLevel)
	},
}

func init() {
	// --env 与 APP_ENV 是仅有的两个有权选择多环境文件的输入
	rootCmd.Flags().StringVar(&configPath, "config", "configs/config.yaml",
		"基础配置文件路径")
	rootCmd.Flags().StringVar(&envFlag, "env", os.Getenv("APP_ENV"),
		"运行环境，选定多环境文件（缺省读 APP_ENV）")
	rootCmd.Flags().StringVar(&logLevel, "log-level", "",
		"静态覆盖 log.level（置于合并栈顶，进程内不变）")
}

// Execute 执行根命令；失败时错误直写 stderr、退出码 1。
func Execute() {
	rootCmd.SilenceErrors = true
	rootCmd.SilenceUsage = true
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
