package forwarder

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// 模拟UDP服务器
type mockUDPServer struct {
	conn    *net.UDPConn
	addr    *net.UDPAddr
	running bool
	ctx     context.Context
	cancel  context.CancelFunc
}

func newMockUDPServer(port int) (*mockUDPServer, error) {
	ctx, cancel := context.WithCancel(context.Background())
	addr := &net.UDPAddr{
		IP:   net.ParseIP("127.0.0.1"),
		Port: port,
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		cancel()
		return nil, err
	}

	server := &mockUDPServer{
		conn:   conn,
		addr:   addr,
		ctx:    ctx,
		cancel: cancel,
	}

	go server.start()
	fmt.Printf("Mock UDP server started on port %d\n", port)
	return server, nil
}

func (s *mockUDPServer) start() {
	s.running = true
	buf := make([]byte, 2048)
	for s.running {
		s.conn.SetReadDeadline(time.Now().Add(time.Second))
		n, remoteAddr, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			if !s.running {
				return
			}
			if !strings.Contains(err.Error(), "timeout") {
				fmt.Printf("Mock server 读取错误: %v\n", err)
			}
			continue
		}
		fmt.Printf("Mock server 收到数据: %s 来自: %s\n", string(buf[:n]), remoteAddr.String())

		// 简单的回显服务
		response := fmt.Sprintf("收到消息: %s", string(buf[:n]))
		_, err = s.conn.WriteToUDP([]byte(response), remoteAddr)
		if err != nil {
			fmt.Printf("Mock server 发送响应错误: %v\n", err)
		} else {
			fmt.Printf("Mock server 已发送响应到: %s\n", remoteAddr.String())
		}
	}
}

func (s *mockUDPServer) stop() {
	s.running = false
	s.cancel()
	s.conn.Close()
	fmt.Println("Mock UDP server stopped")
}

// 测试UDP转发器的基本功能
func TestUDPProxy(t *testing.T) {
	// 1. 启动模拟的目标UDP服务器
	targetPort := 15022
	targetServer, err := newMockUDPServer(targetPort)
	assert.NoError(t, err)
	defer targetServer.stop()

	// 2. 创建并启动UDP转发器
	proxyPort := 15023

	proxy, err := CreateUDPProxy(
		"udp",
		"127.0.0.1",
		proxyPort,
		[]string{fmt.Sprintf("127.0.0.1:%d", targetPort)},
		DefaultRelayRuleOptions,
	)
	assert.NoError(t, err)

	err = proxy.Start()
	assert.NoError(t, err)
	defer proxy.Stop()

	// 等待代理启动
	time.Sleep(time.Second)
	fmt.Printf("UDP proxy started on port %d\n", proxyPort)

	// 3. 创建测试客户端
	clientConn, err := net.DialUDP("udp", nil, &net.UDPAddr{
		IP:   net.ParseIP("127.0.0.1"),
		Port: proxyPort,
	})
	assert.NoError(t, err)
	defer clientConn.Close()
	fmt.Printf("Test client connected to proxy\n")

	// 4. 执行测试用例
	testCases := []struct {
		name    string
		message string
		delay   time.Duration
	}{
		{"简单消息", "Hello, UDP!", 200 * time.Millisecond},
		{"长消息", "这是一个很长的消息" + strings.Repeat("test", 100), 200 * time.Millisecond},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			fmt.Printf("\n开始测试用例: %s\n", tc.name)

			// 发送消息
			fmt.Printf("客户端发送消息: %s\n", tc.message)
			_, err := clientConn.Write([]byte(tc.message))
			assert.NoError(t, err)

			// 使用测试用例指定的延迟
			time.Sleep(tc.delay)

			// 设置读取超时
			clientConn.SetReadDeadline(time.Now().Add(2 * time.Second))

			// 接收响应
			buffer := make([]byte, 4096)
			n, addr, err := clientConn.ReadFromUDP(buffer)
			if err != nil {
				t.Fatalf("读取响应失败: %v", err)
			}

			fmt.Printf("客户端收到响应: %s 来自: %s\n", string(buffer[:n]), addr.String())
			response := string(buffer[:n])
			expectedResponse := fmt.Sprintf("收到消息: %s", tc.message)
			assert.Equal(t, expectedResponse, response)
		})

		// 测试用例之间添加间隔
		time.Sleep(100 * time.Millisecond)
	}
}

// 测试UDP转发器的配置选项
func TestUDPProxyConfiguration(t *testing.T) {
	tests := []struct {
		name    string
		options RelayRuleOptions
		check   func(*testing.T, *UDPProxy)
	}{
		{
			name:    "默认配置",
			options: RelayRuleOptions{},
			check: func(t *testing.T, p *UDPProxy) {
				assert.Equal(t, _DefaultUDPPackageSize, p.GetUDPPacketSize())
				assert.Equal(t, int64(_TCPUDPDefaultSingleProxyMaxConnections), p.SingleProxyMaxConnections)
			},
		},
		{
			name: "自定义包大小",
			options: RelayRuleOptions{
				UDPPackageSize: 2048,
			},
			check: func(t *testing.T, p *UDPProxy) {
				assert.Equal(t, int(2048), p.GetUDPPacketSize())
			},
		},
		{
			name: "性能模式",
			options: RelayRuleOptions{
				UDPProxyPerformanceMode: true,
			},
			check: func(t *testing.T, p *UDPProxy) {
				assert.True(t, p.Upm)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proxy, err := CreateUDPProxy(
				"udp",
				"127.0.0.1",
				0, // 使用0让系统分配端口
				[]string{"127.0.0.1:12345"},
				tt.options,
			)
			assert.NoError(t, err)
			tt.check(t, proxy)
		})
	}
}

// 测试并发连接
func TestUDPProxyConcurrent(t *testing.T) {
	// 启动目标服务器
	targetPort := 15024
	targetServer, err := newMockUDPServer(targetPort)
	assert.NoError(t, err)
	defer targetServer.stop()

	// 创建并启动代理
	proxyPort := 15025
	proxy, err := CreateUDPProxy(
		"udp",
		"127.0.0.1",
		proxyPort,
		[]string{fmt.Sprintf("127.0.0.1:%d", targetPort)},
		RelayRuleOptions{
			UDPProxyPerformanceMode: true,
		},
	)
	assert.NoError(t, err)

	err = proxy.Start()
	assert.NoError(t, err)
	defer proxy.Stop()

	// 并发测试
	concurrentClients := 10
	messageCount := 5
	done := make(chan bool, concurrentClients)

	for i := 0; i < concurrentClients; i++ {
		go func(clientID int) {
			conn, err := net.DialUDP("udp", nil, &net.UDPAddr{
				IP:   net.ParseIP("127.0.0.1"),
				Port: proxyPort,
			})
			if err != nil {
				t.Errorf("客户端 %d 连接错误: %v", clientID, err)
				done <- false
				return
			}
			defer conn.Close()

			for j := 0; j < messageCount; j++ {
				message := fmt.Sprintf("Client %d Message %d", clientID, j)
				_, err := conn.Write([]byte(message))
				if err != nil {
					t.Errorf("客户端 %d 发送消息错误: %v", clientID, err)
					done <- false
					return
				}

				conn.SetReadDeadline(time.Now().Add(2 * time.Second))
				buffer := make([]byte, 1024)
				_, _, err = conn.ReadFromUDP(buffer)
				if err != nil {
					t.Errorf("客户端 %d 接收响应错误: %v", clientID, err)
					done <- false
					return
				}
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

	assert.Equal(t, concurrentClients, successCount, "并发测试应该全部成功")
}

// 测试关闭和重启
func TestUDPProxyRestartAndStop(t *testing.T) {
	proxyPort := 15026
	proxy, err := CreateUDPProxy(
		"udp",
		"127.0.0.1",
		proxyPort,
		[]string{"127.0.0.1:12345"},
		RelayRuleOptions{},
	)
	assert.NoError(t, err)

	// 测试启动
	err = proxy.Start()
	assert.NoError(t, err)
	assert.True(t, proxy.IsRunning())

	// 等待一小段时间确保启动完成
	time.Sleep(100 * time.Millisecond)

	// 测试停止
	err = proxy.Stop()
	assert.NoError(t, err)
	assert.False(t, proxy.IsRunning())

	// 等待一小段时间确保完全停止
	time.Sleep(100 * time.Millisecond)

	// 测试重启
	err = proxy.Restart()
	if err != nil {
		t.Logf("重启失败: %v", err)
		t.Logf("当前代理状态: running=%v", proxy.IsRunning())
	}
	assert.NoError(t, err)
	assert.True(t, proxy.IsRunning())

	// 最后停止
	time.Sleep(100 * time.Millisecond)
	err = proxy.Stop()
	assert.NoError(t, err)
}
