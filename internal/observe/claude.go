package observe

import "encoding/json"

type claudeDecoder struct{ decoderState }

func (n *claudeDecoder) Decode(r Record, base Event) []Event {
	var row struct {
		Type       string          `json:"type"`
		Timestamp  string          `json:"timestamp"`
		SessionID  string          `json:"sessionId"`
		UUID       string          `json:"uuid"`
		ParentUUID string          `json:"parentUuid"`
		Message    json.RawMessage `json:"message"`
	}
	if json.Unmarshal(r.Data, &row) != nil || row.Type == "" {
		return []Event{invalid(base)}
	}
	base.NativeType = row.Type
	setTimestamp(&base, row.Timestamp)
	if row.SessionID != "" {
		n.sessionID = row.SessionID
	}
	base.SessionID = n.sessionID
	base.NativeID, base.ParentID = row.UUID, row.ParentUUID
	if row.Type == "user" || row.Type == "assistant" {
		return n.message(base, row.Message, row.Type)
	}
	return []Event{unsupported(base)}
}
