// Package safe 提供日志与展示侧的安全整形：敏感值脱敏（掩码）与限长
// （截断）。全部为纯函数、零第三方依赖、永不 panic——非法输入（负长度、
// 越界上限）一律安全退化。
//
// 掩码两型（均按 rune 计，输出恒为合法 UTF-8）：
//
//   - Mask 保长：138****5678——保留量级与类型辨识度，代价是泄露长度
//     档位（电话/卡号长度属公开知识，可接受）；
//   - MaskFixed 折叠：abcd****wxyz——掩码段长度由参数指定、与原长解耦
//     （零量级泄露），适合 token/密码等长度本身敏感的场景；maskLen 取值
//     注意与被掩段原长的差距——过小则辨识度趋同于保长，过大则掩码段
//     比原文还长（输出膨胀），惯例取 4。
//
// 截断两语义（类型即用途，不互相污染）：
//
//   - Truncate / TruncateBytes 展示向：输出至多 max 字节、切口对齐 rune
//     边界（永不产生非法 UTF-8），截断时以 "..."（计入 max）标示；
//   - Cut 体积向：纯字节硬切、零拷贝子切片，面向二进制流与体积上限。
package safe

import (
	"strings"
	"unicode/utf8"
)

const ellipsis = "..." // 截断标示（三字节，计入 max 预算）

// Mask 保长脱敏：保留前 keepStart、后 keepEnd 个字符（rune 计），其余
// 替换为 *，总长度（rune 数）不变。保留数之和 ≥ 原长时全掩——短串不因
// 保留规则泄露全文。keepStart / keepEnd 为负按 0 计；空串返回空串。
//
//	Mask("13812345678", 3, 4) → "138****5678"
//	Mask("秘密", 3, 3)        → "**"（保不住即全掩）
func Mask(s string, keepStart, keepEnd int) string {
	if keepStart < 0 {
		keepStart = 0
	}
	if keepEnd < 0 {
		keepEnd = 0
	}
	r := []rune(s)
	n := len(r)
	if n == 0 {
		return ""
	}
	if keepStart+keepEnd >= n {
		return strings.Repeat("*", n)
	}
	var b strings.Builder
	b.WriteString(string(r[:keepStart]))
	b.WriteString(strings.Repeat("*", n-keepStart-keepEnd))
	b.WriteString(string(r[n-keepEnd:]))
	return b.String()
}

// MaskFixed 折叠脱敏：保留前 keepStart、后 keepEnd 个字符，中间替换为
// maskLen 个 *——掩码段长度与原长解耦，输出不泄露被掩内容的量级。
// 保留数之和 ≥ 原长时退化为保长全掩（同 Mask 的短串防线）；maskLen ≤ 0
// 时中间段消失（输出即保留的头尾相连）。取值差距注意见包注释。
//
//	MaskFixed("ghp_16CharSecret", 4, 4, 4) → "ghp_****ret"
//	MaskFixed("ab", 4, 4, 4)              → "**"（保不住即全掩）
func MaskFixed(s string, keepStart, keepEnd, maskLen int) string {
	if keepStart < 0 {
		keepStart = 0
	}
	if keepEnd < 0 {
		keepEnd = 0
	}
	if maskLen < 0 {
		maskLen = 0
	}
	r := []rune(s)
	n := len(r)
	if n == 0 {
		return ""
	}
	if keepStart+keepEnd >= n {
		return strings.Repeat("*", n)
	}
	var b strings.Builder
	b.WriteString(string(r[:keepStart]))
	if maskLen > 0 {
		b.WriteString(strings.Repeat("*", maskLen))
	}
	b.WriteString(string(r[n-keepEnd:]))
	return b.String()
}

// Truncate 展示截断：输出至多 max 字节（"..." 计入），切口对齐 rune 边界
// ——永不超长、永不产生非法 UTF-8。未超长原样返回；max 小于省略号宽度
// 时退化为无标示的纯前缀截断；max ≤ 0 返回空串。
//
//	Truncate("hello world", 8) → "hello..."
//	Truncate("你好世界", 9)    → "你好..."（切口不切半个汉字）
func Truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return truncateTail(s, max)
}

// truncateTail 对已确认超长的内容做带省略号的前缀截断（rune 边界回退）。
func truncateTail(s string, max int) string {
	if max <= 0 {
		return ""
	}
	limit := max - len(ellipsis)
	if limit <= 0 { // 省略号放不下：纯前缀截断（同样对齐 rune 边界）
		return s[:runeBoundary(s, max)]
	}
	return s[:runeBoundary(s, limit)] + ellipsis
}

// runeBoundary 返回不超过 end 的最大 rune 边界（回退跨字节字符的中间）。
func runeBoundary(s string, end int) int {
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return end
}

// TruncateBytes 是 Truncate 的 []byte 版：同展示语义（≤ max 字节、rune
// 边界、截断附 "..."）。未超长原样返回（共享底层数组）；附省略号时必然
// 重新分配——返回切片不与入参共享底层数组，追加写入不会污染原值。
func TruncateBytes(b []byte, max int) []byte {
	if len(b) <= max {
		return b
	}
	if max <= 0 {
		return nil
	}
	limit := max - len(ellipsis)
	if limit <= 0 {
		return b[:runeBoundaryBytes(b, max)]
	}
	end := runeBoundaryBytes(b, limit)
	// 三索引限 cap=end：append 必然换底层数组，原 b 的后续字节不被覆写
	return append(b[:end:end], ellipsis...)
}

// runeBoundaryBytes 是 runeBoundary 的 []byte 版。
func runeBoundaryBytes(b []byte, end int) int {
	for end > 0 && !utf8.RuneStart(b[end]) {
		end--
	}
	return end
}

// Cut 体积截断：返回 b 的前 max 字节子切片（零拷贝，共享底层数组——
// 对结果做 append 可能覆写 b 的后续内容，需要隔离时自行 copy）。面向
// 二进制流与体积上限，不做 rune 对齐；max ≤ 0 返回 nil，越界按 len(b)。
func Cut(b []byte, max int) []byte {
	if max <= 0 {
		return nil
	}
	if len(b) <= max {
		return b
	}
	return b[:max]
}
