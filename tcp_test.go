package forwarder

import (
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// 模拟TCP服务器
type mockTCPServer struct {
	listener net.Listener
	running  bool
}

func newMockTCPServer(port int) (*mockTCPServer, error) {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}

	server := &mockTCPServer{
		listener: listener,
		running:  true,
	}

	go server.start()
	fmt.Printf("Mock TCP server started on port %d\n", port)
	return server, nil
}

func (s *mockTCPServer) start() {
	for s.running {
		conn, err := s.listener.Accept()
		if err != nil {
			if !strings.Contains(err.Error(), "use of closed network connection") {
				fmt.Printf("Mock server accept error: %v\n", err)
			}
			return
		}
		go s.handleConnection(conn)
	}
}

func (s *mockTCPServer) handleConnection(conn net.Conn) {
	defer conn.Close()
	buf := make([]byte, 1024)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			return
		}
		fmt.Printf("Mock server received: %s\n", string(buf[:n]))
		conn.Write([]byte("OK"))
	}
}

func (s *mockTCPServer) stop() {
	s.running = false
	s.listener.Close()
}

// 测试TCP代理配置
func TestTCPProxyConfiguration(t *testing.T) {
	tests := []struct {
		name    string
		options RelayRuleOptions
		check   func(*testing.T, *TCPProxy)
	}{
		{
			name:    "默认配置",
			options: RelayRuleOptions{},
			check: func(t *testing.T, p *TCPProxy) {
				assert.Equal(t, int64(_TCPUDPDefaultSingleProxyMaxConnections), p.SingleProxyMaxConnections)
				assert.Equal(t, SafeModeNone, p.GetSafeMode())
			},
		},
		{
			name: "自定义配置",
			options: RelayRuleOptions{
				SingleProxyMaxTCPConnections: 100,
				SafeMode:                     "whitelist",
			},
			check: func(t *testing.T, p *TCPProxy) {
				assert.Equal(t, int64(100), p.SingleProxyMaxConnections)
				assert.Equal(t, "whitelist", p.GetSafeMode())
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proxy, err := CreateTCPProxy(
				"tcp",
				"127.0.0.1",
				15000,
				[]string{"127.0.0.1:12345"},
				tt.options,
			)
			assert.NoError(t, err)
			tt.check(t, proxy)
		})
	}
}

// 测试TCP代理基本功能
func TestTCPProxyBasicFunctionality(t *testing.T) {
	// 启动模拟服务器
	mockServer, err := newMockTCPServer(12345)
	assert.NoError(t, err)
	defer mockServer.stop()

	// 创建代理
	proxy, err := CreateTCPProxy(
		"tcp",
		"127.0.0.1",
		15001,
		[]string{"127.0.0.1:12345"},
		RelayRuleOptions{},
	)
	assert.NoError(t, err)

	// 启动代理
	err = proxy.Start()
	assert.NoError(t, err)
	defer proxy.Stop()

	// 测试连接
	conn, err := net.Dial("tcp", "127.0.0.1:15001")
	assert.NoError(t, err)
	defer conn.Close()

	// 发送数据
	_, err = conn.Write([]byte("Hello"))
	assert.NoError(t, err)

	// 读取响应
	buf := make([]byte, 1024)
	conn.SetReadDeadline(time.Now().Add(time.Second))
	n, err := conn.Read(buf)
	assert.NoError(t, err)
	assert.Equal(t, "OK", string(buf[:n]))
}

// 测试TCP代理重启功能
func TestTCPProxyRestartAndStop(t *testing.T) {
	proxy, err := CreateTCPProxy(
		"tcp",
		"127.0.0.1",
		15002,
		[]string{"127.0.0.1:12345"},
		RelayRuleOptions{},
	)
	assert.NoError(t, err)

	// 测试启动
	err = proxy.Start()
	assert.NoError(t, err)
	assert.True(t, proxy.IsRunning())

	// 测试停止
	err = proxy.Stop()
	assert.NoError(t, err)
	assert.False(t, proxy.IsRunning())

	// 测试重启
	err = proxy.Restart()
	assert.NoError(t, err)
	assert.True(t, proxy.IsRunning())

	// 最后停止
	err = proxy.Stop()
	assert.NoError(t, err)
}

// 测试并发连接
func TestTCPProxyConcurrentConnections(t *testing.T) {
	// 启动模拟服务器
	mockServer, err := newMockTCPServer(12346)
	assert.NoError(t, err)
	defer mockServer.stop()

	// 创建代理
	proxy, err := CreateTCPProxy(
		"tcp",
		"127.0.0.1",
		15003,
		[]string{"127.0.0.1:12346"},
		RelayRuleOptions{
			SingleProxyMaxTCPConnections: 10,
		},
	)
	assert.NoError(t, err)

	// 启动代理
	err = proxy.Start()
	assert.NoError(t, err)
	defer proxy.Stop()

	// 并发测试
	concurrentClients := 5
	done := make(chan bool, concurrentClients)

	for i := 0; i < concurrentClients; i++ {
		go func(id int) {
			conn, err := net.Dial("tcp", "127.0.0.1:15003")
			if err != nil {
				t.Logf("Client %d failed to connect: %v", id, err)
				done <- false
				return
			}
			defer conn.Close()

			// 发送数据
			_, err = conn.Write([]byte(fmt.Sprintf("Hello from client %d", id)))
			if err != nil {
				t.Logf("Client %d failed to write: %v", id, err)
				done <- false
				return
			}

			// 读取响应
			buf := make([]byte, 1024)
			conn.SetReadDeadline(time.Now().Add(time.Second))
			_, err = conn.Read(buf)
			if err != nil {
				t.Logf("Client %d failed to read: %v", id, err)
				done <- false
				return
			}

			done <- true
		}(i)
	}

	// 等待所有客户端完成
	successCount := 0
	for i := 0; i < concurrentClients; i++ {
		if <-done {
			successCount++
		}
	}

	assert.Equal(t, concurrentClients, successCount, "所有并发连接应该成功")
}
