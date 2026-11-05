package omprpctest

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
)

type frameWriter struct {
	mu     sync.Mutex
	w      io.Writer
	v2     bool
	chunks int
}

func (fw *frameWriter) send(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakeomp: marshal frame:", err)
		return
	}
	fw.mu.Lock()
	defer fw.mu.Unlock()
	fw.writeLocked(b)
}

func (fw *frameWriter) writeLocked(b []byte) {
	if len(b)+1 <= maxFrameBytes || !fw.v2 {
		_, _ = fw.w.Write(append(b, '\n'))
		return
	}
	fw.chunks++
	id := fmt.Sprintf("rpc-%d", fw.chunks)
	count := (len(b) + chunkSegmentBytes - 1) / chunkSegmentBytes
	for i := range count {
		seg := b[i*chunkSegmentBytes : min((i+1)*chunkSegmentBytes, len(b))]
		c, _ := json.Marshal(map[string]any{
			"type":       "rpc_chunk",
			"chunkId":    id,
			"index":      i,
			"count":      count,
			"byteLength": len(b),
			"data":       base64.StdEncoding.EncodeToString(seg),
		})
		_, _ = fw.w.Write(append(c, '\n'))
	}
}

func (fw *frameWriter) respond(id, command string, data any, errMsg string) {
	r := map[string]any{"type": "response", "command": command, "success": errMsg == ""}
	if id != "" {
		r["id"] = id
	}
	if errMsg != "" {
		r["error"] = errMsg
	} else if data != nil {
		r["data"] = data
	}
	b, err := json.Marshal(r)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakeomp: marshal response:", err)
		return
	}
	fw.mu.Lock()
	defer fw.mu.Unlock()
	limit := maxFrameBytes
	if fw.v2 {
		limit = maxReassembledFrameBytes
	}
	if len(b)+1 > limit {
		delete(r, "data")
		r["success"] = false
		r["error"] = "RPC response exceeded the transport limit"
		b, _ = json.Marshal(r)
	}
	fw.writeLocked(b)
}

func (fw *frameWriter) enableV2() {
	fw.mu.Lock()
	fw.v2 = true
	fw.mu.Unlock()
}

type command struct {
	ID                string `json:"id"`
	Type              string `json:"type"`
	ProtocolVersion   *int   `json:"protocolVersion"`
	Version           *int   `json:"version"`
	Message           string `json:"message"`
	SessionDir        string `json:"sessionDir"`
	Provider          string `json:"provider"`
	ModelID           string `json:"modelId"`
	Level             string `json:"level"`
	StreamingBehavior string `json:"streamingBehavior"`
}
