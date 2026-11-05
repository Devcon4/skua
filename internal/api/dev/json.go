package dev

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
)

const maxBodyBytes = 1 << 20

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("invalid body: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("invalid body: trailing data after the JSON value")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "encode response", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write(body)
}

func serverError(w http.ResponseWriter, r *http.Request, op string, err error) {
	ctrllog.FromContext(r.Context()).Error(err, op)
	http.Error(w, op+" failed", http.StatusInternalServerError)
}
