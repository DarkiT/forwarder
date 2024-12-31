package forwarder

import (
	"sync"
	"time"
)

type rateLimiter struct {
	lastUpdate time.Time
	speed      float64 // 每秒允许的字节数
	precision  int64   // 精度，单位为秒
	freeCap    float64 // 当前可用流量
	maxFreeCap float64 // 最大可用流量
	mtx        sync.Mutex
}

func newRateLimiter(speed float64) *rateLimiter {
	speed = speed * 1024 // 将速率从 KB/s 转换为 B/s
	return &rateLimiter{
		speed:      speed,
		precision:  1, // 每秒更新一次
		lastUpdate: time.Now(),
		maxFreeCap: speed,
		freeCap:    speed,
	}
}

// AllowN 方法用于判断当前是否允许发送指定大小的数据（单位：字节）
func (rl *rateLimiter) AllowN(now time.Time, size float64) bool {
	if rl.speed <= 0 {
		return true
	}

	rl.mtx.Lock()
	defer rl.mtx.Unlock()

	// 计算过去的时间，并恢复流量
	elapsed := now.Sub(rl.lastUpdate)
	rl.freeCap += elapsed.Seconds() * rl.speed
	if rl.freeCap > rl.maxFreeCap {
		rl.freeCap = rl.maxFreeCap
	}

	// 如果流量不足，返回 false
	if rl.freeCap < size {
		return false
	}

	// 扣除已用流量
	rl.freeCap -= size
	rl.lastUpdate = now

	return true
}

// Limit 方法返回限速器的速率，单位为 KB/s
func (rl *rateLimiter) Limit() float64 {
	return rl.speed / 1024
}
