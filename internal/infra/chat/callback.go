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
