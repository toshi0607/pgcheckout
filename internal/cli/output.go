package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"

	"github.com/toshi0607/pgtwig/internal/config"
)

func writeResult(out io.Writer, format string, value any) error {
	if format == "json" {
		if err := json.NewEncoder(out).Encode(value); err != nil {
			return fmt.Errorf("write JSON result: %w", err)
		}
		return nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode text result: %w", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return fmt.Errorf("decode text result fields: %w", err)
	}
	for _, key := range slices.Sorted(maps.Keys(fields)) {
		if _, err := fmt.Fprintf(out, "%s\t%s\n", key, fields[key]); err != nil {
			return fmt.Errorf("write text result: %w", err)
		}
	}
	return nil
}

func writeError(out io.Writer, format string, err error) int {
	problem, ok := errors.AsType[*config.Error](err)
	if !ok {
		problem = config.NewError(config.Internal, "could not complete the operation", "")
	}
	if format == "text" {
		fmt.Fprintln(out, "error:", problem.Message)
		if problem.Hint != "" {
			fmt.Fprintln(out, "hint:", problem.Hint)
		}
	} else {
		payload := struct {
			Error *config.Error `json:"error"`
		}{Error: problem}
		json.NewEncoder(out).Encode(payload)
	}
	if problem.Code == config.Usage {
		return 2
	}
	return 1
}

func writeRefillWarning(out io.Writer, format string) {
	if format == "text" {
		fmt.Fprintln(out, "warning: could not start detached pool refill (refill_started=false)")
		return
	}
	json.NewEncoder(out).Encode(struct {
		Warning       string `json:"warning"`
		RefillStarted bool   `json:"refill_started"`
	}{Warning: "could not start detached pool refill"})
}

func writeReleaseWarning(out io.Writer, format string) {
	if format == "text" {
		fmt.Fprintln(out, "warning: could not release detached pool refill process")
		return
	}
	json.NewEncoder(out).Encode(map[string]string{"warning": "could not release detached pool refill process"})
}
