package runner

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/google/renameio/v2"

	"github.com/Devcon4/skua/internal/contract/wire"
)

// state is /skua/run/state.json; it outlives container restarts.
type state struct {
	Assign      *wire.Assign       `json:"assign,omitempty"`
	Prepared    bool               `json:"prepared,omitempty"`
	Prompted    bool               `json:"prompted,omitempty"`
	Restarts    int                `json:"restarts,omitempty"`
	Submission  json.RawMessage    `json:"submission,omitempty"`
	Result      *wire.RunnerResult `json:"result,omitempty"`
	Finished    bool               `json:"finished,omitempty"`
	LastCommand int64              `json:"lastCommand,omitempty"`
}

func (r *runner) update(fn func(st *state)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	fn(&r.st)
	if err := r.saveLocked(); err != nil {
		slog.Error("runner could not persist its state", "path", r.l.statePath(), "err", err)
	}
}

func (r *runner) loadState() error {
	b, err := os.ReadFile(r.l.statePath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var st state
	if err := json.Unmarshal(b, &st); err != nil {
		return err
	}
	r.mu.Lock()
	r.st = st
	r.mu.Unlock()
	return nil
}

func (r *runner) saveLocked() error {
	b, err := json.Marshal(r.st)
	if err != nil {
		return err
	}
	pf, err := renameio.NewPendingFile(r.l.statePath(), renameio.WithStaticPermissions(0o600), renameio.WithTempDir(filepath.Dir(r.l.statePath())))
	if err != nil {
		return err
	}
	defer func() { _ = pf.Cleanup() }()
	if _, err := io.Copy(pf, bytes.NewReader(b)); err != nil {
		return err
	}
	return pf.CloseAtomicallyReplace()
}
