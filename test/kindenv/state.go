package kindenv

import (
	"encoding/json"
	"fmt"
	"os"
)

// State is what Up records in StateFile once the environment is ready.
type State struct {
	Name                string             `json:"name"`
	Kubeconfig          string             `json:"kubeconfig"`
	EnvFile             string             `json:"envFile"`
	WallSeconds         int                `json:"wallSeconds"`
	RuntimeClassHandler string             `json:"runtimeClassHandler"`
	RunscPlatforms      map[string]string  `json:"runscPlatforms"`
	PeakNodeMemoryMiB   map[string]float64 `json:"peakNodeMemoryMiB"`
}

func writeState(s State) error {
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(StateFile(s.Name), append(raw, '\n'), 0o644)
}

// ReadState reads the State Up wrote for cluster name. It returns an error wrapping
// os.ErrNotExist when Up has not finished for that cluster.
func ReadState(name string) (State, error) {
	var s State
	raw, err := os.ReadFile(StateFile(name))
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return s, fmt.Errorf("%s: %w", StateFile(name), err)
	}
	return s, nil
}
