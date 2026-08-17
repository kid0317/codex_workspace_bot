package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
)

type readiness struct {
	Receivers map[string]struct {
		State string `json:"state"`
	} `json:"receivers"`
}

func main() {
	expected := flag.Int("expected", 0, "expected enabled receiver count")
	flag.Parse()
	if err := checkReceivers(os.Stdin, *expected); err != nil {
		fmt.Fprintln(os.Stderr, "receivercheck:", err)
		os.Exit(1)
	}
}

func checkReceivers(reader io.Reader, expected int) error {
	if expected < 1 {
		return fmt.Errorf("expected receiver count must be positive")
	}
	payload, err := io.ReadAll(io.LimitReader(reader, (1<<20)+1))
	if err != nil {
		return fmt.Errorf("read readiness JSON: %w", err)
	}
	if len(payload) > 1<<20 {
		return fmt.Errorf("readiness JSON exceeds 1 MiB")
	}
	var status readiness
	if err := json.Unmarshal(payload, &status); err != nil {
		return fmt.Errorf("decode readiness JSON: %w", err)
	}
	if status.Receivers == nil {
		return fmt.Errorf("receivers object is missing")
	}
	if len(status.Receivers) != expected {
		return fmt.Errorf("receiver count is %d, want %d", len(status.Receivers), expected)
	}
	for id, receiver := range status.Receivers {
		if receiver.State != "connected" {
			return fmt.Errorf("receiver %q state is %q", id, receiver.State)
		}
	}
	return nil
}
