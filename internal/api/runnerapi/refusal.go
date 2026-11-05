package runnerapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/Devcon4/skua/internal/contract/wire"
)

type refusal struct {
	code   int
	reason string
	detail string
}

func (e *refusal) Error() string {
	return e.reason + ": " + e.detail
}

func decode(w http.ResponseWriter, r *http.Request, limit int64, v any) error {
	return json.NewDecoder(http.MaxBytesReader(w, r.Body, limit)).Decode(v)
}

func decodeRefusal(err error) *refusal {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return &refusal{http.StatusRequestEntityTooLarge, wire.RefusalBodyTooLarge, fmt.Sprintf("body exceeds %d bytes", tooLarge.Limit)}
	}
	return &refusal{http.StatusBadRequest, wire.RefusalBadRequest, "invalid body: " + err.Error()}
}

func refuse(w http.ResponseWriter, ref *refusal) {
	http.Error(w, ref.reason+": "+ref.detail, ref.code)
}
