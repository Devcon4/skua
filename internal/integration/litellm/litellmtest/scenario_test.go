package litellmtest

import (
	"os"
	"testing"
)

func parse(t *testing.T, doc string) Scenario {
	t.Helper()
	s, err := ParseScenario([]byte(doc))
	if err != nil {
		t.Fatalf("ParseScenario: %v", err)
	}
	return s
}

func TestParseScenarioRejectsInvalid(t *testing.T) {
	cases := map[string]string{
		"unknown field":     "name: a\nmatch: b\nturns: [{txt: hi}]\n",
		"unknown fault key": "name: a\nmatch: b\nturns: [{fault: {stauts: 429}}]\n",
		"missing match":     "name: a\nturns: [{text: hi}]\n",
		"bad arguments":     "name: a\nmatch: b\nturns: [{toolCalls: [{name: t, arguments: [1, 2]}]}]\n",
		"bad status":        "name: a\nmatch: b\nturns: [{fault: {status: 200}}]\n",
		"bad delay":         "name: a\nmatch: b\nturns: [{fault: {firstByteDelay: soon}}]\n",
		"empty fault":       "name: a\nmatch: b\nturns: [{fault: {times: 1}}]\n",
	}
	for name, doc := range cases {
		if _, err := ParseScenario([]byte(doc)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	s := parse(t, "name: a\nmatch: b\nturns: [{toolCalls: [{name: t, arguments: '{\"k\": 1}'}]}]\n")
	if args, err := s.Turns[0].ToolCalls[0].argumentsJSON(); err != nil || args != `{"k":1}` {
		t.Fatalf("string arguments = %q, %v", args, err)
	}
}

func TestE2EScenarioFilesLoad(t *testing.T) {
	scenarios, err := LoadFS(os.DirFS("../../../../test/fakes/llm/scenarios"), "*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, s := range scenarios {
		names[s.Name] = true
	}
	if !names["smoke"] {
		t.Fatalf("scenarios = %v", names)
	}
}
