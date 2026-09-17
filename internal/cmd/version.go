package cmd

import (
	"fmt"

	"go_template/internal/app"

	"github.com/spf13/cobra"
)

// versionCmd 打印构建期注入的版本字符串（仅一行，不带 name/env 前缀）。
var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "打印版本（构建期注入，缺省 dev）",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println(app.Version)
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
