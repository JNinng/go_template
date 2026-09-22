package instance

import (
	"os"
	"testing"
)

// ID 解析优先级：INSTANCE_ID > {service}:{HOSTNAME} > 裸 hostname
// （service 空）> unknown 尾段。
func TestID(t *testing.T) {
	setenv := func(k, v string) (restore func()) {
		old, had := os.LookupEnv(k)
		if v == "" {
			os.Unsetenv(k)
		} else {
			os.Setenv(k, v)
		}
		return func() {
			if had {
				os.Setenv(k, old)
			} else {
				os.Unsetenv(k)
			}
		}
	}
	t.Run("explicit instance id wins", func(t *testing.T) {
		r1 := setenv("INSTANCE_ID", "custom-1")
		defer r1()
		r2 := setenv("HOSTNAME", "pod-abc")
		defer r2()
		if got := ID("svc"); got != "custom-1" {
			t.Fatalf("ID = %q", got)
		}
	})
	t.Run("service colon hostname", func(t *testing.T) {
		r1 := setenv("INSTANCE_ID", "")
		defer r1()
		r2 := setenv("HOSTNAME", "pod-abc")
		defer r2()
		if got := ID("svc"); got != "svc:pod-abc" {
			t.Fatalf("ID = %q", got)
		}
		if got := ID(""); got != "pod-abc" {
			t.Fatalf("ID(no service) = %q", got)
		}
	})
	t.Run("unknown fallback", func(t *testing.T) {
		r1 := setenv("INSTANCE_ID", "")
		defer r1()
		r2 := setenv("HOSTNAME", "")
		defer r2()
		if got := ID("svc"); got != "svc:unknown" {
			t.Fatalf("ID = %q", got)
		}
	})
}
