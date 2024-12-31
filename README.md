# 端口转发工具 (Port Forwarder)

一个高性能的端口转发工具，支持 TCP 和 UDP 协议，提供完整的流量控制、安全检查和监控功能。

## 主要特性

### 1. 协议支持
- TCP 转发（支持 TCP/TCP4/TCP6）
- UDP 转发（支持 UDP/UDP4/UDP6）
- 支持多目标地址的负载均衡转发

### 2. 性能优化
- TCP 连接优化
  - 自适应缓冲区大小（128KB - 256KB）
  - TCP_NODELAY 和 KeepAlive 优化
  - 连接池复用机制
- UDP 高性能模式
  - 多工作协程处理（可配置）
  - 基于 channel 的数据包分发
  - 内置缓冲区池，减少内存分配
- 全局资源管理
  - 统一的缓冲区池管理
  - 连接数限制和监控
  - 协程数量控制

### 3. 安全特性
- IP 访问控制
  - 支持白名单模式
  - 支持黑名单模式
  - 本地回环地址自动允许
- 流量控制
  - 入站/出站流量限速
  - 全局和单端口流量限制
  - 基于令牌桶算法的限速实现
- 连接保护
  - 全局最大连接数限制（默认 1024）
  - 单端口最大连接数限制（默认 256）
  - 异常连接自动清理（30s 超时）

### 4. 监控功能
- 流量统计
  - 实时入站/出站流量
  - 流量速率监控
  - 数据包统计
- 连接监控
  - 当前活跃连接数
  - TCP/UDP 会话状态
  - 连接建立/断开事件
- 性能指标
  - 处理延迟统计
  - 缓冲区使用情况
  - 协程数量监控

## 配置说明

### 1. 全局配置
```go
DefaultRelayRuleOptions = RelayRuleOptions{
    // UDP 配置
    UDPPackageSize: 65507,                    // UDP 包最大大小
    UDPBufferSize: 4 * 1024 * 1024,          // UDP 缓冲区大小（4MB）
    UDPQueueSize: 10000,                      // UDP 队列大小
    UDPProxyPerformanceMode: true,            // UDP 性能模式
    
    // TCP 配置
    SingleProxyMaxTCPConnections: 256,        // 单端口最大 TCP 连接数
    
    // 安全配置
    SafeMode: SafeModeNone,                   // 安全模式（none/white/black）
    
    // 流量控制
    InRateLimit: 0,                           // 入站流量限制（0表示不限制）
    OutRateLimit: 0,                          // 出站流量限制（0表示不限制）
}
```

### 2. 代理创建选项
```go
CreateProxyOptions{
    ProxyType: "tcp",              // 协议类型
    ListenIP: "0.0.0.0",          // 监听地址
    ListenPort: 8080,             // 监听端口
    TargetAddressList: []string{   // 目标地址列表
        "192.168.1.100:80",
    },
    RelayOptions: RelayRuleOptions{
        // 自定义转发规则选项
    },
}
```

## API 接口

### REST API
- `GET /forwarders` - 获取所有转发器列表
- `POST /forwarders` - 创建新的转发器
- `PUT /forwarders/{type}/{id}/status` - 更新转发器状态
- `PUT /forwarders/{type}/{id}/ratelimit` - 更新转发器流量限制
- `DELETE /forwarders/{type}/{id}` - 删除转发器

## 使用示例

### 1. 创建 TCP 转发
```go
manager := forwarder.NewManager()
proxy, err := manager.CreateProxy(CreateProxyOptions{
    ProxyType: "tcp",
    ListenIP: "0.0.0.0",
    ListenPort: 8080,
    TargetAddressList: []string{"192.168.1.100:80"},
})
if err != nil {
    log.Fatal(err)
}
proxy.Start()
```

### 2. 创建 UDP 转发（性能模式）
```go
proxy, err := manager.CreateProxy(CreateProxyOptions{
    ProxyType: "udp",
    ListenIP: "0.0.0.0",
    ListenPort: 53,
    TargetAddressList: []string{"8.8.8.8:53"},
    RelayOptions: RelayRuleOptions{
        UDPProxyPerformanceMode: true,
        UDPPackageSize: 4096,
    },
})
```

## 性能优化建议

1. TCP 转发优化
- 对于高流量场景，自动启用 256KB 缓冲区
- 启用 TCP_NODELAY 减少延迟
- 设置合适的 KeepAlive 时间（默认 30s）

2. UDP 转发优化
- 高并发场景开启性能模式（UDPProxyPerformanceMode）
- 调整 UDPBufferSize 和 UDPQueueSize
- 根据 CPU 核心数自动设置工作协程数

3. 系统配置
- 调整系统最大文件描述符限制
- 优化网络相关内核参数
- 合理设置全局最大连接数

## 注意事项

1. 安全配置
- 建议在公网环境启用 IP 白名单模式
- 合理设置流量限制避免资源耗尽
- 定期检查连接状态和资源使用

2. 性能考虑
- UDP 性能模式会增加内存使用
- 大量并发连接时注意系统资源限制
- 监控流量统计避免性能瓶颈

## 许可证
MIT License