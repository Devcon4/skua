package mcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"time"
)

const (
	maxSubmissionBytes = 64 << 20
	dialTimeout        = 5 * time.Second
	readTimeout        = 10 * time.Second
	replyTimeout       = 60 * time.Second
)

// Listener accepts submit_result hand-offs on a unix socket.
type Listener struct {
	ln net.Listener
}

// Submission is one submitted JSON value; the runner answers it with Ack or Reject exactly once.
type Submission struct {
	Value json.RawMessage
	conn  net.Conn
}

type reply struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// Listen removes any stale socket at path and listens on it.
func Listen(path string) (*Listener, error) {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	return &Listener{ln: ln}, nil
}

// Accept blocks until a connection delivers one valid JSON value. Connections that send
// anything else are answered with an error and skipped. It returns an error once the
// listener is closed.
func (l *Listener) Accept() (*Submission, error) {
	for {
		conn, err := l.ln.Accept()
		if err != nil {
			return nil, err
		}
		v, err := readValue(conn)
		if err != nil {
			_ = writeReply(conn, reply{Error: err.Error()})
			_ = conn.Close()
			continue
		}
		return &Submission{Value: v, conn: conn}, nil
	}
}

// Close stops accepting and removes the socket file.
func (l *Listener) Close() error {
	return l.ln.Close()
}

// Ack tells the submitter the value is stored.
func (s *Submission) Ack() error {
	defer func() { _ = s.conn.Close() }()
	return writeReply(s.conn, reply{OK: true})
}

// Reject tells the submitter the value was not stored, with reason shown to the agent.
func (s *Submission) Reject(reason string) error {
	defer func() { _ = s.conn.Close() }()
	return writeReply(s.conn, reply{Error: reason})
}

// Submit hands v to the runner listening at path and returns nil only after the runner
// answered ok; a rejection is returned as an error carrying the runner's reason.
func Submit(path string, v json.RawMessage) error {
	var line bytes.Buffer
	if err := json.Compact(&line, v); err != nil {
		return fmt.Errorf("submission is not JSON: %w", err)
	}
	line.WriteByte('\n')
	conn, err := net.DialTimeout("unix", path, dialTimeout)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(replyTimeout)); err != nil {
		return err
	}
	if _, err := conn.Write(line.Bytes()); err != nil {
		return err
	}
	b, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		return fmt.Errorf("runner did not answer: %w", err)
	}
	var r reply
	if err := json.Unmarshal(b, &r); err != nil {
		return fmt.Errorf("runner answer is not JSON: %w", err)
	}
	if !r.OK {
		if r.Error == "" {
			return errors.New("runner refused the result")
		}
		return errors.New(r.Error)
	}
	return nil
}

func readValue(conn net.Conn) (json.RawMessage, error) {
	if err := conn.SetReadDeadline(time.Now().Add(readTimeout)); err != nil {
		return nil, err
	}
	b, err := bufio.NewReader(io.LimitReader(conn, maxSubmissionBytes)).ReadBytes('\n')
	if err != nil {
		return nil, fmt.Errorf("read submission: %w", err)
	}
	b = bytes.TrimSpace(b)
	if !json.Valid(b) {
		return nil, errors.New("submission is not one JSON value")
	}
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		return nil, err
	}
	return json.RawMessage(b), nil
}

func writeReply(conn net.Conn, r reply) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if err := conn.SetWriteDeadline(time.Now().Add(readTimeout)); err != nil {
		return err
	}
	_, err = conn.Write(append(b, '\n'))
	return err
}
