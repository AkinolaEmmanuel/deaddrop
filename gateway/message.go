package gateway

import "encoding/json"

const (
	ActionJoin     = "join"
	ActionSync     = "sync"
	ActionComplete = "complete"
)

type CryptoPayload struct {
	Ciphertext []byte `json:"ciphertext"`
	IV         []byte `json:"iv"`
	AuthTag    []byte `json:"authTag"`
}

type Message struct {
	Action  string          `json:"action"`
	RoomID  string          `json:"roomId,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

func (m *Message) DecodedPayload() (*CryptoPayload, error) {
	var payload CryptoPayload
	if err := json.Unmarshal(m.Payload, &payload); err != nil {
		return nil, err
	}
	return &payload, nil
}
