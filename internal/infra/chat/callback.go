package chat

// StreamCallback receives streaming chat responses.
// Aligns with Java StreamCallback.
type StreamCallback interface {
	OnContent(content string)
	OnThinking(content string)
	OnComplete()
	OnError(err error)
}

// StreamHandle allows cancelling an in-progress stream.
// Aligns with Java StreamCancellationHandle.
type StreamHandle interface {
	Cancel()
	Wait()
}

// noopStreamCallback is a no-op implementation for testing.
type noopStreamCallback struct{}

func (n *noopStreamCallback) OnContent(string)  {}
func (n *noopStreamCallback) OnThinking(string) {}
func (n *noopStreamCallback) OnComplete()       {}
func (n *noopStreamCallback) OnError(error)     {}
