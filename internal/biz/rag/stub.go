package rag

import "context"

// StubService is a minimal Service implementation for testing and bootstrapping.
// The real pipeline implementation comes in 2B-3.
type StubService struct{}

func (s *StubService) StreamChat(ctx context.Context, question, conversationID, taskID string, deepThinking bool, sender *SSESender) {
	sender.SendMessage(MsgTypeResponse, "stub: "+question)
	sender.SendFinish("stub-msg", "")
	sender.SendDone()
	sender.Close()
}

func (s *StubService) StopTask(taskID string) {}
