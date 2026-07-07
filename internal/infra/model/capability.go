package model

// Capability represents a type of AI model capability.
// Aligns with Java ModelCapability.
type Capability struct {
	name        string
	displayName string
}

func (c Capability) String() string      { return c.name }
func (c Capability) DisplayName() string { return c.displayName }

var (
	CapabilityChat      = Capability{"chat", "Chat"}
	CapabilityEmbedding = Capability{"embedding", "Embedding"}
	CapabilityRerank    = Capability{"rerank", "Rerank"}
	CapabilityVLM       = Capability{"vlm", "VLM"}
)
