package hatch

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
)

const (
	maxPlaintextFrame = 65489
	maxChunks         = 256
	maxLogicalFrame   = maxPlaintextFrame * maxChunks
	maxResponseBytes  = 4 << 20
	maxFileBytes      = 64 << 20
	maxPendingStreams = 8
	maxFragmentGroups = 16
	fragmentTTL       = 30 * time.Second
)

type field struct {
	n protowire.Number
	t protowire.Type
	v uint64
	b []byte
}

func parseFields(raw []byte) ([]field, error) {
	var out []field
	for len(raw) > 0 {
		n, t, k := protowire.ConsumeTag(raw)
		if k < 0 || n < 1 {
			return nil, fault("protocol", "decode", "malformed protobuf tag")
		}
		raw = raw[k:]
		f := field{n: n, t: t}
		switch t {
		case protowire.VarintType:
			f.v, k = protowire.ConsumeVarint(raw)
		case protowire.BytesType:
			f.b, k = protowire.ConsumeBytes(raw)
		default:
			k = protowire.ConsumeFieldValue(n, t, raw)
		}
		if k < 0 {
			return nil, fault("protocol", "decode", "malformed protobuf field")
		}
		raw = raw[k:]
		out = append(out, f)
		if len(out) > 4096 {
			return nil, fault("protocol", "decode", "too many protobuf fields")
		}
	}
	return out, nil
}
func putVar(b []byte, n protowire.Number, v uint64) []byte {
	b = protowire.AppendTag(b, n, protowire.VarintType)
	return protowire.AppendVarint(b, v)
}
func putBytes(b []byte, n protowire.Number, v []byte) []byte {
	b = protowire.AppendTag(b, n, protowire.BytesType)
	return protowire.AppendBytes(b, v)
}
func fieldBytes(raw []byte, number protowire.Number) ([]byte, error) {
	fields, err := parseFields(raw)
	if err != nil {
		return nil, err
	}
	var result []byte
	found := false
	for _, f := range fields {
		if f.n == number {
			if f.t != protowire.BytesType || found {
				return nil, fault("protocol", "decode", "invalid envelope")
			}
			result = f.b
			found = true
		}
	}
	if !found {
		return nil, fault("protocol", "decode", "missing envelope payload")
	}
	return result, nil
}
func encodeRequest(id uint64, method, path string, body []byte) []byte {
	var req []byte
	req = putBytes(req, 1, []byte(method))
	req = putBytes(req, 2, []byte(path))
	headers := [][2]string{{"x-request-id", newID()}, {"x-app-id", "hatch-web"}, {"Accept-Language", "en-US"}, {"Accept-Encoding", "identity"}}
	if body != nil {
		headers = append(headers, [2]string{"Content-Type", "application/json"})
	}
	for _, h := range headers {
		v := putBytes(nil, 1, []byte(h[0]))
		v = putBytes(v, 2, []byte(h[1]))
		req = putBytes(req, 3, v)
	}
	if body != nil {
		req = putBytes(req, 4, body)
	}
	req = putVar(req, 5, 1)
	frame := putVar(nil, 1, id)
	frame = putBytes(frame, 2, req)
	// ServiceRequest.service=SERVICE_DAEMON (0), payload field 2.
	return putBytes(nil, 2, frame)
}
func encodeReset(id uint64) []byte {
	r := putVar(nil, 1, 1)
	f := putVar(nil, 1, id)
	f = putBytes(f, 5, r)
	return putBytes(nil, 2, f)
}

type packet struct {
	id         uint64
	status     int
	data       []byte
	end, reset bool
}

func decodeResponse(raw []byte) (packet, error) {
	var p packet
	// ServiceResponse carries ServiceFrame in field 1 in the current wire
	// implementation. A few hatch builds wrap the same frame in field 2, so
	// accept that envelope only when it contains a valid stream-id frame.
	outer, err := parseFields(raw)
	if err != nil {
		return p, err
	}
	var frame []byte
	for _, f := range outer {
		if f.t == protowire.BytesType && (f.n == 1 || f.n == 2) {
			candidate, e := parseFields(f.b)
			if e != nil {
				continue
			}
			for _, cf := range candidate {
				if cf.n == 1 && cf.t == protowire.VarintType && cf.v != 0 {
					frame = f.b
					break
				}
			}
			if frame != nil {
				break
			}
		}
	}
	if frame == nil {
		// Keep a useful diagnostic for malformed envelopes rather than treating
		// the outer service field as a stream id.
		return p, fault("protocol", "decode", "missing response frame")
	}
	fs, err := parseFields(frame)
	if err != nil {
		return p, err
	}
	var kind field
	found := false
	for _, f := range fs {
		if f.n == 1 {
			if f.t != protowire.VarintType {
				return p, fault("protocol", "decode", "invalid stream ID")
			}
			p.id = f.v
		}
		if f.n >= 2 && f.n <= 5 {
			if found || f.t != protowire.BytesType {
				return p, fault("protocol", "decode", "invalid service kind")
			}
			kind = f
			found = true
		}
	}
	if !found || p.id == 0 || kind.n == 2 {
		return p, fault("protocol", "decode", "invalid service response")
	}
	if kind.n == 5 {
		p.reset = true
		p.end = true
		return p, nil
	}
	body, err := parseFields(kind.b)
	if err != nil {
		return p, err
	}
	for _, f := range body {
		switch {
		case kind.n == 3 && f.n == 1:
			if f.t != protowire.VarintType || f.v < 100 || f.v > 599 {
				return p, fault("protocol", "decode", "invalid HTTP status")
			}
			p.status = int(f.v)
		case (kind.n == 3 && f.n == 3) || (kind.n == 4 && f.n == 1):
			if f.t != protowire.BytesType {
				return p, fault("protocol", "decode", "invalid body chunk")
			}
			p.data = f.b
		case (kind.n == 3 && f.n == 4) || (kind.n == 4 && f.n == 2):
			if f.t != protowire.VarintType {
				return p, fault("protocol", "decode", "invalid end marker")
			}
			p.end = f.v != 0
		}
	}
	if kind.n == 3 && p.status == 0 {
		return p, fault("protocol", "decode", "missing HTTP status")
	}
	return p, nil
}

type fragments struct {
	total   uint64
	parts   map[uint64][]byte
	size    int
	created time.Time
}
type reassembler struct {
	groups map[uint64]*fragments
	size   int
}

func (r *reassembler) push(raw []byte, now time.Time) ([]byte, error) {
	fs, err := parseFields(raw)
	if err != nil {
		return nil, err
	}
	var id, index, total uint64
	var data []byte
	var mask uint8
	for _, f := range fs {
		if f.n >= 1 && f.n <= 4 {
			bit := uint8(1 << (f.n - 1))
			if mask&bit != 0 {
				return nil, fault("protocol", "fragment", "duplicate metadata")
			}
			mask |= bit
			if f.n < 4 && f.t != protowire.VarintType || f.n == 4 && f.t != protowire.BytesType {
				return nil, fault("protocol", "fragment", "invalid metadata")
			}
			switch f.n {
			case 1:
				id = f.v
			case 2:
				index = f.v
			case 3:
				total = f.v
			case 4:
				data = f.b
			}
		}
	}
	if mask != 15 || total < 1 || total > maxChunks || index >= total || len(data) > maxPlaintextFrame {
		return nil, fault("protocol", "fragment", "invalid fragment bounds")
	}
	if r.groups == nil {
		r.groups = make(map[uint64]*fragments)
	}
	for _, g := range r.groups {
		if now.Sub(g.created) > fragmentTTL {
			return nil, fault("protocol", "fragment", "fragment assembly timed out")
		}
	}
	g := r.groups[id]
	if g == nil {
		if len(r.groups) >= maxFragmentGroups {
			return nil, fault("protocol", "fragment", "too many incomplete messages")
		}
		g = &fragments{total: total, parts: make(map[uint64][]byte), created: now}
		r.groups[id] = g
	}
	if g.total != total {
		return nil, fault("protocol", "fragment", "inconsistent fragment count")
	}
	if _, ok := g.parts[index]; ok {
		return nil, fault("protocol", "fragment", "duplicate fragment")
	}
	if r.size+len(data) > maxLogicalFrame {
		return nil, fault("protocol", "fragment", "fragment buffer limit exceeded")
	}
	g.parts[index] = bytes.Clone(data)
	g.size += len(data)
	r.size += len(data)
	if uint64(len(g.parts)) != total {
		return nil, nil
	}
	out := make([]byte, 0, g.size)
	for i := uint64(0); i < total; i++ {
		out = append(out, g.parts[i]...)
	}
	delete(r.groups, id)
	r.size -= g.size
	return out, nil
}
func pack(raw []byte) ([][]byte, error) {
	if len(raw) > maxLogicalFrame {
		return nil, fault("protocol", "encode", "message exceeds fragment limit")
	}
	var token [8]byte
	if _, err := rand.Read(token[:]); err != nil {
		return nil, fault("protocol", "encode", "random source failed")
	}
	id := binary.BigEndian.Uint64(token[:])
	total := (len(raw) + maxPlaintextFrame - 1) / maxPlaintextFrame
	if total == 0 {
		total = 1
	}
	out := make([][]byte, 0, total)
	for i := 0; i < total; i++ {
		start := i * maxPlaintextFrame
		end := start + maxPlaintextFrame
		if end > len(raw) {
			end = len(raw)
		}
		b := putVar(nil, 1, id)
		b = putVar(b, 2, uint64(i))
		b = putVar(b, 3, uint64(total))
		b = putBytes(b, 4, raw[start:end])
		out = append(out, b)
	}
	return out, nil
}
