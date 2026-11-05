package omprpc

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
)

type chunkAssembly struct {
	id         string
	count      int
	byteLength int
	next       int
	buf        []byte
}

func (c *chunkAssembly) add(line []byte) ([]byte, error) {
	var f struct {
		ChunkID    string `json:"chunkId"`
		Index      int    `json:"index"`
		Count      int    `json:"count"`
		ByteLength int    `json:"byteLength"`
		Data       string `json:"data"`
	}
	if err := json.Unmarshal(line, &f); err != nil {
		return nil, fmt.Errorf("decode rpc_chunk: %w", err)
	}
	if f.Index == 0 {
		if f.Count < 1 || f.ByteLength < 1 || f.ByteLength > maxReassembledBytes {
			return nil, fmt.Errorf("rpc_chunk %s: count %d, byteLength %d out of range", f.ChunkID, f.Count, f.ByteLength)
		}
		*c = chunkAssembly{id: f.ChunkID, count: f.Count, byteLength: f.ByteLength, buf: make([]byte, 0, f.ByteLength)}
	} else if f.ChunkID != c.id || f.Index != c.next || f.Count != c.count {
		return nil, fmt.Errorf("rpc_chunk %s index %d out of sequence", f.ChunkID, f.Index)
	}
	seg, err := base64.StdEncoding.DecodeString(f.Data)
	if err != nil {
		return nil, fmt.Errorf("rpc_chunk %s: %w", f.ChunkID, err)
	}
	if len(c.buf)+len(seg) > c.byteLength {
		return nil, fmt.Errorf("rpc_chunk %s exceeds byteLength %d", f.ChunkID, c.byteLength)
	}
	c.buf = append(c.buf, seg...)
	c.next++
	if c.next < c.count {
		return nil, nil
	}
	if len(c.buf) != c.byteLength {
		return nil, fmt.Errorf("rpc_chunk %s: got %d bytes, want %d", f.ChunkID, len(c.buf), c.byteLength)
	}
	frame := c.buf
	*c = chunkAssembly{}
	return frame, nil
}
