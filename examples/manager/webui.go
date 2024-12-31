package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	proxy "github.com/darkit/forwarder"
	"github.com/darkit/slog"
	"github.com/gorilla/mux"
)

const configFile = "forwarders.json"

//go:embed index.html all:layui
var dist embed.FS

var ctx = context.Background()

// Forwarder 配置文件结构体
type Forwarder struct {
	ID             int       `json:"id"`                     // 转发器ID
	Type           string    `json:"type"`                   // 转发类型(tcp/udp)
	LocalAddr      string    `json:"localAddr"`              // 本地监听地址
	RemoteAddr     string    `json:"remoteAddr"`             // 远程目标地址
	TrafficIn      int64     `json:"trafficIn"`              // 入站流量统计(字节)
	TrafficOut     int64     `json:"trafficOut"`             // 出站流量统计(字节)
	TrafficInRate  float64   `json:"trafficInRate"`          // 入站速率(字节/秒)
	TrafficOutRate float64   `json:"trafficOutRate"`         // 出站速率(字节/秒)
	RateLimit      RateLimit `json:"rateLimit"`              // 流量限制(KB/s)
	UpdateTime     int64     `json:"updateTime"`             // 最后更新时间戳
	Description    string    `json:"description"`            // 转发器描述
	Status         bool      `json:"status"`                 // 转发器状态(启用/禁用)
	AllRateLimit   float64   `json:"allRateLimit,omitempty"` // 全局流量限制(KB/s)
}

type RateLimit struct {
	LimitIn  float64 `json:"limitIn"`
	LimitOut float64 `json:"limitOut"`
}

var (
	logger = slog.Default("Forwarder")
	pm     = proxy.GetGlobalManager()
	svr    = pm.GetManager()
)

func init() {
	slog.SetLevelInfo()
	slog.NewLogger(os.Stdout, false, true)
	pm.GetOrSetLogger(logger)
}

// APIResponse 定义统一的API响应格式
type APIResponse struct {
	Code  int         `json:"code"`
	Msg   string      `json:"msg"`
	Count int         `json:"count"`
	Data  interface{} `json:"data"`
}

// NewAPIResponse 创建新的API响应
func NewAPIResponse(code int, msg string, count int, data interface{}) *APIResponse {
	return &APIResponse{
		Code:  code,
		Msg:   msg,
		Count: count,
		Data:  data,
	}
}

func main() {
	r := mux.NewRouter()

	r.HandleFunc("/", serveHTML)
	r.HandleFunc("/forwarders", getForwarders).Methods("GET")
	r.HandleFunc("/forwarders", addForwarder).Methods("POST")
	r.HandleFunc("/forwarders/{type}/{id}/ratelimit", updateForwarderRateLimit).Methods("PUT")
	r.HandleFunc("/forwarders/{type}/{id}", deleteForwarder).Methods("DELETE")
	r.HandleFunc("/forwarders/{type}/{id}/status", updateForwarderStatus).Methods("PUT")

	r.PathPrefix("/layui/").Handler(http.StripPrefix("/", http.FileServer(http.FS(dist))))

	go loadForwarders()

	logger.Println("Starting server on :8080")
	http.ListenAndServe(":8080", r)
}

func serveHTML(w http.ResponseWriter, r *http.Request) {
	content, _ := dist.ReadFile("index.html")

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(200)
	w.Write(content)
}

func getForwarders(w http.ResponseWriter, r *http.Request) {
	proxyList := svr.GetAllProxiesList()
	forwarders := make([]Forwarder, 0, len(proxyList))

	for _, p := range proxyList {
		forwarder := Forwarder{
			ID:             p.ID,
			Type:           strings.ToUpper(p.Type),
			LocalAddr:      p.ListenAddr,
			RemoteAddr:     p.RemoteAddr,
			TrafficIn:      p.TrafficIn,
			TrafficOut:     p.TrafficOut,
			TrafficInRate:  p.RateIn,
			TrafficOutRate: p.RateOut,
			RateLimit: RateLimit{
				LimitIn:  p.RateLimit.In,
				LimitOut: p.RateLimit.Out,
			},
			UpdateTime:  p.UpdateTime.Unix(),
			Description: p.Description,
			Status:      p.Status,
		}
		forwarders = append(forwarders, forwarder)
	}

	// 按 ID 排序
	sort.Slice(forwarders, func(i, j int) bool {
		return forwarders[i].ID < forwarders[j].ID
	})

	response := NewAPIResponse(0, "获取成功", len(forwarders), forwarders)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func updateForwarderStatus(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id, err := strconv.Atoi(vars["id"])
	if err != nil {
		json.NewEncoder(w).Encode(NewAPIResponse(400, "无效的转发器ID", 0, nil))
		return
	}

	proxyType := strings.ToLower(vars["type"])
	if proxyType != "tcp" && proxyType != "udp" {
		http.Error(w, "Invalid proxy type", http.StatusBadRequest)
		return
	}

	var updateData struct {
		Status bool `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&updateData); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if p, exists := svr.GetProxy(id, proxyType); exists {
		if updateData.Status {
			err = p.Restart()
		} else {
			err = p.Stop()
		}

		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		json.NewEncoder(w).Encode(NewAPIResponse(0, "更新成功", 1, map[string]interface{}{
			"id":     id,
			"status": p.GetStatus(),
		}))
	} else {
		http.Error(w, "Forwarder not found", http.StatusNotFound)
	}
}

func addForwarder(w http.ResponseWriter, r *http.Request) {
	var forwarder Forwarder
	if err := json.NewDecoder(r.Body).Decode(&forwarder); err != nil {
		json.NewEncoder(w).Encode(NewAPIResponse(400, err.Error(), 0, nil))
		return
	}

	// 解析地址
	host, portStr, err := net.SplitHostPort(forwarder.LocalAddr)
	if err != nil {
		http.Error(w, "Invalid local address format", http.StatusBadRequest)
		return
	}

	port, err := strconv.Atoi(portStr)
	if err != nil {
		http.Error(w, "Invalid port number", http.StatusBadRequest)
		return
	}

	// 创建代理选项
	opts := proxy.CreateProxyOptions{
		ProxyType:         forwarder.Type,
		ListenIP:          host,
		ListenPort:        port,
		TargetAddressList: []string{strings.ReplaceAll("：", ":", forwarder.RemoteAddr)},
		RelayOptions: proxy.RelayRuleOptions{
			InRateLimit:  forwarder.AllRateLimit,
			OutRateLimit: forwarder.AllRateLimit,
			Description:  forwarder.Description,
		},
	}

	// 创建并启动代理
	proxy, err := svr.CreateProxy(opts)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}

	if err = proxy.Start(); err != nil {
		var netErr *net.OpError
		if errors.As(err, &netErr) && netErr.Op == "listen" {
			http.Error(w, "端口已被占用", http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// 返回代理信息
	proxyInfo := svr.GetAllProxiesList()[proxy.GetID()]
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(NewAPIResponse(0, "创建成功", 1, proxyInfo))

	saveForwarders()
}

func deleteForwarder(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id, err := strconv.Atoi(vars["id"])
	if err != nil {
		json.NewEncoder(w).Encode(NewAPIResponse(400, "无效的转发器ID", 0, nil))
		return
	}

	proxyType := strings.ToLower(vars["type"])
	if proxyType != "tcp" && proxyType != "udp" {
		http.Error(w, "Invalid proxy type", http.StatusBadRequest)
		return
	}

	if p, exists := svr.GetProxy(id, proxyType); exists {
		p.Stop()
		svr.RemoveProxy(id, proxyType)
		w.WriteHeader(http.StatusNoContent)
	} else {
		http.Error(w, "Forwarder not found", http.StatusNotFound)
	}

	json.NewEncoder(w).Encode(NewAPIResponse(0, "删除成功", 0, nil))

	saveForwarders()
}

func updateForwarderRateLimit(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id, err := strconv.Atoi(vars["id"])
	if err != nil {
		json.NewEncoder(w).Encode(NewAPIResponse(400, "无效的转发器ID", 0, nil))
		return
	}

	proxyType := strings.ToLower(vars["type"])
	if proxyType != "tcp" && proxyType != "udp" {
		http.Error(w, "Invalid proxy type", http.StatusBadRequest)
		return
	}

	var updateData struct {
		In  json.Number `json:"limitIn"`
		Out json.Number `json:"limitOut"`
	}
	if err := json.NewDecoder(r.Body).Decode(&updateData); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	in, err := updateData.In.Float64()
	if err != nil {
		http.Error(w, "Invalid rate limit value", http.StatusBadRequest)
		return
	}

	out, err := updateData.Out.Float64()
	if err != nil {
		http.Error(w, "Invalid rate limit value", http.StatusBadRequest)
		return
	}
	if p, exists := svr.GetProxy(id, proxyType); exists {
		p.SetRateLimit(in, out)

		response := NewAPIResponse(0, "更新成功", 1, map[string]interface{}{
			"id":         id,
			"type":       proxyType,
			"status":     p.GetStatus(),
			"updateTime": time.Now().Unix(),
		})

		json.NewEncoder(w).Encode(response)

		saveForwarders()
	} else {
		http.Error(w, "Forwarder not found", http.StatusNotFound)
	}
}

func loadForwarders() {
	data, err := os.ReadFile(configFile)
	if err != nil {
		if os.IsNotExist(err) {
			logger.Println("Config file not found. Starting with empty configuration.")
			return
		}
		logger.Fatalf("Error reading config file: %v", err)
	}

	var configs []Forwarder
	if err := json.Unmarshal(data, &configs); err != nil {
		logger.Fatalf("Error unmarshalling config: %v", err)
	}

	for _, cfg := range configs {
		// 解析监听地址
		host, portStr, err := net.SplitHostPort(cfg.LocalAddr)
		if err != nil {
			logger.Printf("Error parsing listen address: %v", err)
			continue
		}

		port, err := strconv.Atoi(portStr)
		if err != nil {
			logger.Printf("Invalid port number: %v", err)
			continue
		}

		// 创建代理选项
		opts := proxy.CreateProxyOptions{
			ProxyType:         strings.ToLower(cfg.Type),
			ListenIP:          host,
			ListenPort:        port,
			TargetAddressList: []string{cfg.RemoteAddr},
			RelayOptions: proxy.RelayRuleOptions{
				InRateLimit:  cfg.RateLimit.LimitIn,
				OutRateLimit: cfg.RateLimit.LimitOut,
				Description:  cfg.Description,
			},
		}
		// 创建代理
		p, err := svr.CreateProxy(opts)
		if err != nil {
			logger.Printf("Error creating proxy: %v", err)
			continue
		}

		// 如果配置为启用状态，则启动代理
		if cfg.Status {
			if err = p.Start(); err != nil {
				logger.Printf("Error starting proxy: %v", err)
			}
		}
	}
	logger.Infof("Loaded %d forwarders from config", len(configs))
}

func saveForwarders() {
	proxyList := svr.GetAllProxiesList()
	configs := make([]Forwarder, 0, len(proxyList))

	for _, p := range proxyList {
		configs = append(configs, Forwarder{
			ID:             p.ID,
			Type:           strings.ToUpper(p.Type),
			LocalAddr:      p.ListenAddr,
			RemoteAddr:     p.RemoteAddr,
			TrafficIn:      p.TrafficIn,
			TrafficOut:     p.TrafficOut,
			TrafficInRate:  p.RateIn,
			TrafficOutRate: p.RateOut,
			RateLimit: RateLimit{
				LimitIn:  p.RateLimit.In,
				LimitOut: p.RateLimit.Out,
			},
			UpdateTime:  p.UpdateTime.Unix(),
			Description: p.Description,
			Status:      p.Status,
		})
	}

	data, err := json.MarshalIndent(configs, "", "  ")
	if err != nil {
		logger.Printf("Error marshalling config: %v", err)
		return
	}

	if err := os.WriteFile(configFile, data, 0o644); err != nil {
		logger.Printf("Error writing config file: %v", err)
	}
}
