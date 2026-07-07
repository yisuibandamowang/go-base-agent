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
func (p *defaultFirstPacketProbe) AwaitFirstPacket(bridge *ProbeBridge, timeout time.Duration) (ProbeResult, error) {
	select {
	case result := <-bridge.AwaitResult():
		return result, nil
	case <-time.After(timeout):
		return ProbeResult{Success: false, Error: fmt.Errorf("stream first packet timeout after %v", timeout)}, nil
	}
}
