package hatch

// A subscription can replay workspace media without device_id. Only a device
// match, or a message_id confirmed by such a match, attributes it to this task.
// Keep a bounded window for presentations that arrive before their confirmation.
type generationAttribution struct {
	nodeID   string
	messages map[string]bool
	order    []string
	pending  []unattributedMedia
}

type unattributedMedia struct {
	messageID string
	media     Attachment
}

func (a *generationAttribution) accept(name string, payload map[string]any) (bool, []Attachment) {
	device := text(object(payload["chat_context"]), "device_id")
	if device != "" && device != a.nodeID {
		return false, nil
	}
	messageID := text(payload, "message_id")
	ours := device == a.nodeID || (messageID != "" && a.messages[messageID])
	var media []Attachment
	switch name {
	case "delta.presentation", "delta.message_done", "delta.tool_done", "delta.placeholder":
		media = mediaCandidates(payload, 0)
	}
	if !ours {
		if messageID != "" {
			for _, candidate := range media {
				if len(a.pending) == 128 {
					copy(a.pending, a.pending[1:])
					a.pending = a.pending[:127]
				}
				a.pending = append(a.pending, unattributedMedia{messageID, candidate})
			}
		}
		return false, nil
	}
	if messageID != "" {
		if a.messages == nil {
			a.messages = make(map[string]bool)
		}
		if !a.messages[messageID] {
			if len(a.order) == 256 {
				delete(a.messages, a.order[0])
				copy(a.order, a.order[1:])
				a.order = a.order[:255]
			}
			a.messages[messageID] = true
			a.order = append(a.order, messageID)
		}
		var confirmed []Attachment
		remaining := a.pending[:0]
		for _, candidate := range a.pending {
			if candidate.messageID == messageID {
				confirmed = append(confirmed, candidate.media)
			} else {
				remaining = append(remaining, candidate)
			}
		}
		clear(a.pending[len(remaining):])
		a.pending = remaining
		media = append(confirmed, media...)
	}
	return true, media
}
