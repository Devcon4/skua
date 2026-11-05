package wire

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestRunRoundTrip(t *testing.T) {
	created := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	cases := map[string]json.RawMessage{
		"object": json.RawMessage(`{"summary":"done","files":["a.go","b.go"],"count":2}`),
		"string": json.RawMessage(`"plain text result"`),
	}
	for name, result := range cases {
		t.Run(name, func(t *testing.T) {
			in := Run{
				Name:      "research-0123456789",
				Profile:   "research",
				ClientKey: "ck-1",
				Phase:     "Succeeded",
				Reason:    "Completed",
				Result:    result,
				Outputs:   Outputs{Branch: "skua/research-0123456789", PullRequest: 7},
				CreatedAt: created,
			}
			b, err := json.Marshal(in)
			if err != nil {
				t.Fatal(err)
			}
			var out Run
			if err := json.Unmarshal(b, &out); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(in, out) {
				t.Fatalf("round trip differs:\n in: %+v\nout: %+v", in, out)
			}
			var generic map[string]any
			if err := json.Unmarshal(b, &generic); err != nil {
				t.Fatal(err)
			}
			var want any
			if err := json.Unmarshal(result, &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(generic["result"], want) {
				t.Fatalf("result = %#v, want %#v", generic["result"], want)
			}
		})
	}
}
