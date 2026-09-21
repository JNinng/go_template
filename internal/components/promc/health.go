package promc

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// CheckFunc 单项健康检查：返回 nil 即健康；返回 error 即异常
// （error 消息进 details，聚合状态翻转为 unhealthy）。
type CheckFunc func() error

// Status 是健康检查的聚合状态。
type Status string

const (
	StatusHealthy   Status = "healthy"
	StatusUnhealthy Status = "unhealthy"
)

// CheckResult 是 /health 的 JSON 响应体。
type CheckResult struct {
	Status    Status            `json:"status"`
	Timestamp string            `json:"timestamp"`
	Details   map[string]string `json:"details,omitempty"`
}

// Handler 聚合命名检查为单一健康端点（实现 http.Handler）：全部通过
// 200，任一失败 503；零登记恒 healthy（空 details）。仅接受 GET。
type Handler struct {
	mu     sync.RWMutex
	checks []namedCheck
}

type namedCheck struct {
	name  string
	check CheckFunc
}

// NewHandler 返回空检查表的健康端点。
func NewHandler() *Handler { return &Handler{} }

// Register 登记一项命名检查（重名并存——聚合语义下后登记者同样执行）。
func (h *Handler) Register(name string, fn CheckFunc) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.checks = append(h.checks, namedCheck{name: name, check: fn})
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet) // RFC 9110：405 须携带 Allow
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	h.mu.RLock()
	checks := h.checks
	h.mu.RUnlock()

	overall := StatusHealthy
	details := make(map[string]string, len(checks))
	for _, c := range checks {
		if err := c.check(); err != nil {
			overall = StatusUnhealthy
			details[c.name] = err.Error()
		} else {
			details[c.name] = "ok"
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if overall == StatusUnhealthy {
		w.WriteHeader(http.StatusServiceUnavailable)
	} else {
		w.WriteHeader(http.StatusOK)
	}
	_ = json.NewEncoder(w).Encode(CheckResult{
		Status:    overall,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Details:   details,
	})
}
