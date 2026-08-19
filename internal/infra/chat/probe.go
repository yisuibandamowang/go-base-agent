package chat

import (
	"fmt"
	"time"
)

// defaultFirstPacketProbe implements FirstPacketProbe with timeout logic.
type defaultFirstPacketProbe struct{}

// NewFirstPacketProbe creates a new FirstPacketProbe.
func NewFirstPacketProbe() FirstPacketProbe {
	return &defaultFirstPacketProbe{}
}

// AwaitFirstPacket waits for the first successful packet or timeout/error.
// 返回 error 表示等待过程本身异常（如请求取消）；探测失败通过 ProbeResult.Success=false 表达。
func (p *defaultFirstPacketProbe) AwaitFirstPacket(bridge *ProbeBridge, timeout time.Duration) (ProbeResult, error) {
	if ctx := bridge.Context(); ctx != nil {
		select {
		case result := <-bridge.AwaitResult():
			return result, nil
		case <-ctx.Done():
			return ProbeResult{}, ctx.Err()
		case <-time.After(timeout):
			return ProbeResult{Success: false, Error: fmt.Errorf("stream first packet timeout after %v", timeout)}, nil
		}
	}
	select {
	case result := <-bridge.AwaitResult():
		return result, nil
	case <-time.After(timeout):
		return ProbeResult{Success: false, Error: fmt.Errorf("stream first packet timeout after %v", timeout)}, nil
	}
}
