package safe

import (
	"bytes"
	"testing"
	"unicode/utf8"
)

func TestMask(t *testing.T) {
	cases := []struct {
		name      string
		s         string
		keepStart int
		keepEnd   int
		want      string
	}{
		{"phone", "13812345678", 3, 4, "138****5678"},
		{"keep sum equals len", "1234", 2, 2, "****"},
		{"keep sum over len", "ab", 3, 3, "**"},
		{"chinese runes", "张三丰的秘密", 2, 1, "张三***密"},
		{"emoji runes", "a🎉b🎉c", 1, 1, "a***c"},
		{"negative keeps clamp to zero", "13812345678", -1, 4, "*******5678"},
		{"keep nothing", "secret", 0, 0, "******"},
		{"empty", "", 3, 4, ""},
	}
	for _, c := range cases {
		if got := Mask(c.s, c.keepStart, c.keepEnd); got != c.want {
			t.Errorf("%s: Mask(%q, %d, %d) = %q, want %q", c.name, c.s, c.keepStart, c.keepEnd, got, c.want)
		}
	}
}

func TestMaskFixed(t *testing.T) {
	cases := []struct {
		name      string
		s         string
		keepStart int
		keepEnd   int
		maskLen   int
		want      string
	}{
		{"token fold", "ghp_16CharSecret", 4, 4, 4, "ghp_****cret"},
		{"keep sum equals len falls back full mask", "1234", 2, 2, 4, "****"},
		{"keep sum over len falls back full mask", "ab", 4, 4, 4, "**"},
		{"zero mask len drops middle", "abcdef", 2, 2, 0, "abef"},
		{"negative mask len clamps to zero", "abcdef", 2, 2, -3, "abef"},
		{"empty", "", 4, 4, 4, ""},
	}
	for _, c := range cases {
		if got := MaskFixed(c.s, c.keepStart, c.keepEnd, c.maskLen); got != c.want {
			t.Errorf("%s: MaskFixed(%q, %d, %d, %d) = %q, want %q",
				c.name, c.s, c.keepStart, c.keepEnd, c.maskLen, got, c.want)
		}
	}
	// 长度解耦契约：掩码段长度只由 maskLen 决定，原长不改变输出。
	for _, s := range []string{"x", "short", "an extremely long secret value"} {
		if got := MaskFixed(s, 0, 0, 4); got != "****" {
			t.Errorf("MaskFixed(%q, 0, 0, 4) = %q, want %q (output must not depend on input length)", s, got, "****")
		}
	}
}

func TestTruncate(t *testing.T) {
	cases := []struct {
		name string
		s    string
		max  int
		want string
	}{
		{"under max returns as is", "hello", 10, "hello"},
		{"exactly max returns as is", "hello", 5, "hello"},
		{"ascii cut with ellipsis", "hello world", 8, "hello..."},
		{"ellipsis counted in max", "hello world", 8, "hello..."},
		{"chinese cut at rune boundary", "你好世界", 9, "你好..."},
		{"chinese cut backs off partial rune", "你好世界", 7, "你..."},
		{"max below ellipsis width cuts plainly", "hello", 2, "he"},
		{"max equals ellipsis width cuts plainly", "hello", 3, "hel"},
		{"max zero", "hello", 0, ""},
		{"max negative", "hello", -1, ""},
		{"empty", "", 3, ""},
	}
	for _, c := range cases {
		got := Truncate(c.s, c.max)
		if got != c.want {
			t.Errorf("%s: Truncate(%q, %d) = %q, want %q", c.name, c.s, c.max, got, c.want)
		}
		if len(got) > c.max && c.max > 0 {
			t.Errorf("%s: Truncate(%q, %d) = %q exceeds max", c.name, c.s, c.max, got)
		}
		if !utf8.ValidString(got) {
			t.Errorf("%s: Truncate(%q, %d) = %q is not valid UTF-8", c.name, c.s, c.max, got)
		}
	}
}

func TestTruncateBytes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"under max returns as is", "hello", 10, "hello"},
		{"ascii cut with ellipsis", "hello world", 8, "hello..."},
		{"chinese cut at rune boundary", "你好世界", 9, "你好..."},
		{"max below ellipsis width cuts plainly", "hello", 2, "he"},
		{"max zero", "hello", 0, ""},
	}
	for _, c := range cases {
		got := TruncateBytes([]byte(c.in), c.max)
		if string(got) != c.want {
			t.Errorf("%s: TruncateBytes(%q, %d) = %q, want %q", c.name, c.in, c.max, got, c.want)
		}
	}

	// 超长且附省略号时：重新分配——对结果追加不得污染原切片。
	b := []byte("hello world")
	got := TruncateBytes(b, 8)
	_ = append(got, 'X', 'Y', 'Z')
	if !bytes.Equal(b, []byte("hello world")) {
		t.Errorf("append to TruncateBytes result clobbered original: %q", b)
	}
}

func TestCut(t *testing.T) {
	b := []byte("hello world")

	if got := Cut(b, 5); string(got) != "hello" {
		t.Errorf("Cut over max = %q, want %q", got, "hello")
	}
	if got := Cut(b, len(b)); &got[0] != &b[0] {
		t.Error("Cut within max must return the original slice (zero copy)")
	}
	if got := Cut(b, 1<<20); &got[0] != &b[0] {
		t.Error("Cut with max over len must return the original slice")
	}
	if got := Cut(b, 0); got != nil {
		t.Errorf("Cut max 0 = %q, want nil", got)
	}
	if got := Cut(b, -1); got != nil {
		t.Errorf("Cut negative max = %q, want nil", got)
	}
}
