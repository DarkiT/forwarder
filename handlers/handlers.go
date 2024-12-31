package handlers

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	proxy "github.com/darkit/forwarder"
)

// Handler 是一个泛型接口，定义了所有处理方法
type Handler[T any] interface {
	GetForwarders() T
	AddForwarder() T
	UpdateForwarderStatus() T
	DeleteForwarder() T
	UpdateForwarderRateLimit() T
}

type Forwarder struct {
	ID             int         `json:"id"`             // 转发器ID
	Type           string      `json:"type"`           // 转发类型(tcp/udp)
	LocalAddr      string      `json:"localAddr"`      // 本地监听地址
	RemoteAddr     string      `json:"remoteAddr"`     // 远程目标地址
	TrafficIn      int64       `json:"trafficIn"`      // 入站流量统计(字节)
	TrafficOut     int64       `json:"trafficOut"`     // 出站流量统计(字节)
	TrafficInRate  float64     `json:"trafficInRate"`  // 入站速率(字节/秒)
	TrafficOutRate float64     `json:"trafficOutRate"` // 出站速率(字节/秒)
	RateLimit      float64     `json:"rateLimit"`      // 流量限制(KB/s)
	UpdateTime     int64       `json:"updateTime"`     // 最后更新时间戳
	Description    string      `json:"description"`    // 转发器描述
	Status         bool        `json:"status"`         // 转发器状态(启用/禁用)
	proxy          proxy.Proxy // 底层代理实例
}

// ForwarderHandler 是一个泛型结构体，实现了 Handler 接口
type ForwarderHandler[T any] struct {
	ctx  context.Context
	warp func(http.HandlerFunc) T
	pm   *proxy.GlobalManager
}

// NewForwarderHandler 创建新的处理器实例
func NewForwarderHandler[T any](warp func(http.HandlerFunc) T) *ForwarderHandler[T] {
	return &ForwarderHandler[T]{
		ctx:  context.Background(),
		warp: warp,
		pm:   proxy.GetGlobalManager(),
	}
}

// GetHandlers 返回实现了 Handler 接口的 ForwarderHandler
func (h *ForwarderHandler[T]) GetHandlers() Handler[T] {
	return h
}

// GetForwarders 获取转发器列表
func (h *ForwarderHandler[T]) GetForwarders() T {
	return h.warp(func(w http.ResponseWriter, r *http.Request) {
		proxyList := h.pm.GetManager().GetAllProxiesList()
		jsonResponse(w, http.StatusOK, map[string]interface{}{
			"code":  0,
			"msg":   "数据获取成功",
			"count": len(proxyList),
			"data":  proxyList,
		})
	})
}

// AddForwarder 添加新的转发器
func (h *ForwarderHandler[T]) AddForwarder() T {
	return h.warp(func(w http.ResponseWriter, r *http.Request) {
		var forwarder Forwarder

		if err := json.NewDecoder(r.Body).Decode(&forwarder); err != nil {
			errorResponse(w, http.StatusBadRequest, err.Error())
			return
		}

		// 解析地址
		host, portStr, err := net.SplitHostPort(forwarder.LocalAddr)
		if err != nil {
			errorResponse(w, http.StatusBadRequest, err.Error())
			return
		}

		port, err := strconv.Atoi(portStr)
		if err != nil {
			errorResponse(w, http.StatusBadRequest, "Invalid port number")
			return
		}

		// 创建代理
		opts := proxy.DefaultRelayRuleOptions
		opts.InRateLimit = forwarder.RateLimit
		opts.OutRateLimit = forwarder.RateLimit
		opts.Description = forwarder.Description

		p, err := h.pm.GetManager().CreateProxy(proxy.CreateProxyOptions{
			ProxyType:         forwarder.Type,
			ListenIP:          host,
			ListenPort:        port,
			TargetAddressList: []string{forwarder.RemoteAddr},
			RelayOptions:      opts,
		})
		if err != nil {
			errorResponse(w, http.StatusInternalServerError, err.Error())
			return
		}

		// 启动代理
		if err = p.Start(); err != nil {
			errorResponse(w, http.StatusInternalServerError, err.Error())
			return
		}

		// 构建响应
		jsonResponse(w, http.StatusCreated, map[string]interface{}{
			"id":          p.GetID(),
			"type":        p.GetType(),
			"localAddr":   forwarder.LocalAddr,
			"remoteAddr":  forwarder.RemoteAddr,
			"status":      p.GetStatus(),
			"rateLimit":   forwarder.RateLimit,
			"updateTime":  time.Now().Unix(),
			"description": forwarder.Description,
		})
	})
}

// UpdateForwarderStatus 更新转发器状态
func (h *ForwarderHandler[T]) UpdateForwarderStatus() T {
	return h.warp(func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(getPathVar(r, "id"))
		if err != nil {
			errorResponse(w, http.StatusBadRequest, "Invalid forwarder ID")
			return
		}

		proxyType := getPathVar(r, "type")
		if proxyType != "tcp" && proxyType != "udp" {
			errorResponse(w, http.StatusBadRequest, "Invalid proxy type")
			return
		}

		var updateData struct {
			Status bool `json:"status"`
		}
		if err := json.NewDecoder(r.Body).Decode(&updateData); err != nil {
			errorResponse(w, http.StatusBadRequest, err.Error())
			return
		}

		if p, exists := h.pm.GetManager().GetProxy(id, proxyType); exists {
			if updateData.Status {
				err = p.Restart()
			} else {
				err = p.Stop()
			}

			if err != nil {
				errorResponse(w, http.StatusInternalServerError, err.Error())
				return
			}

			jsonResponse(w, http.StatusOK, map[string]interface{}{
				"id":     id,
				"status": p.GetStatus(),
			})
		} else {
			errorResponse(w, http.StatusNotFound, "Forwarder not found")
		}
	})
}

// DeleteForwarder 删除转发器
func (h *ForwarderHandler[T]) DeleteForwarder() T {
	return h.warp(func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(getPathVar(r, "id"))
		if err != nil {
			errorResponse(w, http.StatusBadRequest, "Invalid forwarder ID")
			return
		}

		proxyType := getPathVar(r, "type")
		if proxyType != "tcp" && proxyType != "udp" {
			errorResponse(w, http.StatusBadRequest, "Invalid proxy type")
			return
		}

		if p, exists := h.pm.GetManager().GetProxy(id, proxyType); exists {
			p.Stop()
			h.pm.GetManager().RemoveProxy(id, proxyType)
			w.WriteHeader(http.StatusNoContent)
		} else {
			errorResponse(w, http.StatusNotFound, "Forwarder not found")
		}
	})
}

// UpdateForwarderRateLimit 更新转发器速率限制
func (h *ForwarderHandler[T]) UpdateForwarderRateLimit() T {
	return h.warp(func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(getPathVar(r, "id"))
		if err != nil {
			errorResponse(w, http.StatusBadRequest, "Invalid forwarder ID")
			return
		}

		proxyType := getPathVar(r, "type")
		if proxyType != "tcp" && proxyType != "udp" {
			errorResponse(w, http.StatusBadRequest, "Invalid proxy type")
			return
		}

		var updateData struct {
			RateLimit json.Number `json:"rateLimit"`
		}
		if err := json.NewDecoder(r.Body).Decode(&updateData); err != nil {
			errorResponse(w, http.StatusBadRequest, err.Error())
			return
		}

		rateLimit, err := updateData.RateLimit.Float64()
		if err != nil {
			errorResponse(w, http.StatusBadRequest, "Invalid rate limit value")
			return
		}

		if p, exists := h.pm.GetManager().GetProxy(id, proxyType); exists {
			p.SetRateLimit(rateLimit, rateLimit)

			jsonResponse(w, http.StatusOK, map[string]interface{}{
				"id":         id,
				"type":       proxyType,
				"rateLimit":  rateLimit,
				"status":     p.GetStatus(),
				"updateTime": time.Now().Unix(),
			})
		} else {
			errorResponse(w, http.StatusNotFound, "Forwarder not found")
		}
	})
}

// SetupRoutes 设置路由
func (h *ForwarderHandler[T]) SetupRoutes() *http.ServeMux {
	mux := http.NewServeMux()

	// 通用的路由处理函数
	handleRoute := func(pattern string, handler func() T) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			// 解析路由参数
			vars := parsePathVars(r.URL.Path, pattern)
			// 将参数存储到请求上下文中
			ctx := context.WithValue(r.Context(), "vars", vars)
			r = r.WithContext(ctx)

			h := handler()
			if fn, ok := any(h).(func(http.ResponseWriter, *http.Request)); ok {
				fn(w, r)
			} else {
				errorResponse(w, http.StatusInternalServerError, "Handler type mismatch")
			}
		})
	}

	handleRoute("/forwarders", h.GetForwarders)
	handleRoute("/forwarders/{type}/{id}/ratelimit", h.UpdateForwarderRateLimit)
	handleRoute("/forwarders/{type}/{id}", h.DeleteForwarder)
	handleRoute("/forwarders/{type}/{id}/status", h.UpdateForwarderStatus)

	return mux
}

// 辅助函数
func jsonResponse(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func errorResponse(w http.ResponseWriter, status int, message string) {
	jsonResponse(w, status, map[string]interface{}{
		"code": -1,
		"msg":  message,
	})
}

// 添加路由参数解析辅助函数
func parsePathVars(path, pattern string) map[string]string {
	vars := make(map[string]string)
	pathParts := strings.Split(strings.Trim(path, "/"), "/")
	patternParts := strings.Split(strings.Trim(pattern, "/"), "/")

	if len(pathParts) != len(patternParts) {
		return vars
	}

	for i, pattern := range patternParts {
		if strings.HasPrefix(pattern, "{") && strings.HasSuffix(pattern, "}") {
			key := strings.Trim(pattern, "{}")
			vars[key] = pathParts[i]
		}
	}

	return vars
}

// 添加获取路由参数的辅助函数
func getPathVar(r *http.Request, key string) string {
	if vars, ok := r.Context().Value("vars").(map[string]string); ok {
		return vars[key]
	}
	return ""
}
