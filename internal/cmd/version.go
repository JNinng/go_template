package cmd

import (
	"fmt"

	"go_template/pkg/version"

	"github.com/spf13/cobra"
)

// versionCmd 打印构建期注入的全部版本元数据（version / commit / date /
// build_time / go_version 五字段对齐逐行，不带 name/env 前缀）。
var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "打印版本信息（构建期注入，缺省 dev）",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println(version.String())
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
