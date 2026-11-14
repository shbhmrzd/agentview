package observe

import (
	"bytes"
	"encoding/json"
)

// An oversized line is not validated JSON. Retain only bounded, top-level
// header evidence; never decode replacement history or claim a complete record.
// Headers after the first 16 KiB remain unavailable and keep only a diagnostic.
type partialHeader struct {
	Type, Subtype, Timestamp string
	Ordinal                  *uint64
}

func readPartialHeader(prefix []byte) *partialHeader {
	d := json.NewDecoder(bytes.NewReader(prefix[:min(len(prefix), 16*1024)]))
	if token, err := d.Token(); err != nil || token != json.Delim('{') {
		return nil
	}
	h := &partialHeader{}
	seen := map[string]bool{}
	for fields := 0; fields < 32 && d.More(); fields++ {
		token, err := d.Token()
		if err != nil {
			break
		}
		key, ok := token.(string)
		if !ok {
			break
		}
		if key == "type" || key == "subtype" || key == "timestamp" || key == "ordinal" {
			if seen[key] {
				return nil
			}
			seen[key] = true
			if key == "ordinal" {
				if d.Decode(&h.Ordinal) != nil {
					return nil
				}
				continue
			}
			var value string
			if d.Decode(&value) != nil || len(value) > 128 {
				break
			}
			switch key {
			case "type":
				h.Type = value
			case "subtype":
				h.Subtype = value
			case "timestamp":
				h.Timestamp = value
			}
		} else {
			var discard json.RawMessage
			if d.Decode(&discard) != nil {
				break
			}
		}
	}
	if h.Type == "compacted" || (h.Type == "system" && h.Subtype == "compact_boundary") {
		return h
	}
	return nil
}

func partialCompaction(base Event, h *partialHeader) Event {
	base.Kind, base.NativeType, base.PartialRecord = "context.compacted", h.Type, true
	base.SourceOrdinal = h.Ordinal
	if h.Subtype != "" {
		base.NativeType += ":" + h.Subtype
	}
	setTimestamp(&base, h.Timestamp)
	base.Detail = "Partial record: compaction header observed in an oversized line. The remainder is unparsed; the byte range covers only the observed prefix."
	return base
}

func (n *codexDecoder) DecodePartial(r Record, base Event) []Event {
	h := r.Header
	if h == nil || h.Type != "compacted" {
		return nil
	}
	base = partialCompaction(base, h)
	if n.inherited(h.Ordinal) {
		return []Event{inheritedRecord(base)}
	}
	if n.debug.HistoryStartOrdinal != nil && h.Ordinal == nil {
		base.Kind = "record.ownership_unknown"
		base.Detail = "Oversized compaction has no usable ordinal; ownership cannot be distinguished from inherited history. Marker omitted."
	}
	return []Event{base}
}
func (n *claudeDecoder) DecodePartial(r Record, base Event) []Event {
	h := r.Header
	if h == nil || h.Type != "system" || h.Subtype != "compact_boundary" {
		return nil
	}
	return []Event{partialCompaction(base, h)}
}
