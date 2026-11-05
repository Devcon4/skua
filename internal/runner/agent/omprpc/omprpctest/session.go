package omprpctest

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"sigs.k8s.io/yaml"
)

type sessionState struct {
	ID        string `json:"id"`
	Provider  string `json:"provider,omitempty"`
	ModelID   string `json:"modelId,omitempty"`
	Script    string `json:"script,omitempty"`
	Progress  int    `json:"progress,omitempty"`
	Failures  int    `json:"failures,omitempty"`
	StartedAt int64  `json:"startedAt,omitempty"`
	Crashed   bool   `json:"crashed,omitempty"`
	Finished  bool   `json:"finished,omitempty"`
	LastText  string `json:"lastText,omitempty"`
}

type session struct {
	path  string
	state sessionState
}

func (s *session) save() error {
	if s.path == "" {
		return nil
	}
	b, err := json.Marshal(s.state)
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (a *agent) openSession(c command) {
	if c.SessionDir == "" {
		a.out.respond(c.ID, c.Type, nil, "open_session requires sessionDir")
		return
	}
	if (c.Provider == "") != (c.ModelID == "") {
		a.out.respond(c.ID, c.Type, nil, "open_session requires provider and modelId together")
		return
	}
	if c.Provider != "" {
		if err := checkModel(c.Provider, c.ModelID); err != nil {
			a.out.respond(c.ID, c.Type, nil, err.Error())
			return
		}
	}
	a.stopTurn()
	if err := os.MkdirAll(c.SessionDir, 0o700); err != nil {
		a.out.respond(c.ID, c.Type, nil, err.Error())
		return
	}
	s := &session{path: filepath.Join(c.SessionDir, sessionStateFile)}
	resumed := false
	if b, err := os.ReadFile(s.path); err == nil {
		if err := json.Unmarshal(b, &s.state); err != nil {
			a.out.respond(c.ID, c.Type, nil, "corrupt session file: "+err.Error())
			return
		}
		resumed = true
	} else {
		s.state.ID = randomID()
		s.state.Provider, s.state.ModelID = defaultModel()
	}
	if c.Provider != "" {
		s.state.Provider, s.state.ModelID = c.Provider, c.ModelID
	}
	if err := s.save(); err != nil {
		a.out.respond(c.ID, c.Type, nil, err.Error())
		return
	}
	a.mu.Lock()
	a.session = s
	a.mu.Unlock()
	a.out.respond(c.ID, c.Type, map[string]any{
		"cancelled":   false,
		"resumed":     resumed,
		"sessionId":   s.state.ID,
		"sessionFile": s.path,
	}, "")
}

func randomID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func agentDir() string {
	if d := os.Getenv("PI_CODING_AGENT_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".omp", "agent")
}

type modelsFile struct {
	Providers map[string]provider `json:"providers"`
}

type provider struct {
	BaseURL string `json:"baseUrl"`
	APIKey  string `json:"apiKey"`
	API     string `json:"api"`
	Models  []struct {
		ID string `json:"id"`
	} `json:"models"`
}

func (p provider) has(id string) bool {
	return slices.ContainsFunc(p.Models, func(m struct {
		ID string `json:"id"`
	}) bool {
		return m.ID == id
	})
}

func loadModels() (*modelsFile, error) {
	b, err := os.ReadFile(filepath.Join(agentDir(), "models.yml"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var m modelsFile
	if err := yaml.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("models.yml: %w", err)
	}
	return &m, nil
}

func checkModel(prov, id string) error {
	m, err := loadModels()
	if err != nil {
		return err
	}
	if m == nil {
		return nil
	}
	if p, ok := m.Providers[prov]; ok && p.has(id) {
		return nil
	}
	return fmt.Errorf("Model not found: %s/%s", prov, id) //nolint:staticcheck // ST1005: mirrors omp's error text
}

func defaultModel() (string, string) {
	b, err := os.ReadFile(filepath.Join(agentDir(), "config.yml"))
	if err != nil {
		return "", ""
	}
	var cfg struct {
		ModelRoles map[string]string `json:"modelRoles"`
	}
	if yaml.Unmarshal(b, &cfg) != nil {
		return "", ""
	}
	prov, id, _ := strings.Cut(cfg.ModelRoles["default"], "/")
	return prov, id
}
