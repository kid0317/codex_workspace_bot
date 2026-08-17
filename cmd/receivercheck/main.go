package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type readiness struct {
	Receivers map[string]struct {
		State string `json:"state"`
	} `json:"receivers"`
}

func main() {
	expectedIDs := flag.String("expected-ids", "", "comma-separated enabled receiver internal IDs")
	configPath := flag.String("config", "", "server config path")
	printBaseURL := flag.Bool("print-base-url", false, "print the validated local health base URL")
	flag.Parse()
	if *printBaseURL {
		if *configPath == "" || *expectedIDs != "" {
			flag.Usage()
			os.Exit(2)
		}
		file, err := os.Open(*configPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "receivercheck:", err)
			os.Exit(1)
		}
		defer file.Close()
		baseURL, err := readLocalBaseURL(file)
		if err != nil {
			fmt.Fprintln(os.Stderr, "receivercheck:", err)
			os.Exit(1)
		}
		fmt.Println(baseURL)
		return
	}
	if *configPath != "" || *expectedIDs == "" {
		flag.Usage()
		os.Exit(2)
	}
	if err := checkReceivers(os.Stdin, strings.Split(*expectedIDs, ",")); err != nil {
		fmt.Fprintln(os.Stderr, "receivercheck:", err)
		os.Exit(1)
	}
}

func checkReceivers(reader io.Reader, expectedIDs []string) error {
	if len(expectedIDs) < 1 {
		return fmt.Errorf("at least one expected receiver ID is required")
	}
	expected := make(map[string]struct{}, len(expectedIDs))
	for _, id := range expectedIDs {
		if id == "" || strings.TrimSpace(id) != id || strings.Contains(id, ",") {
			return fmt.Errorf("expected receiver ID is empty or invalid")
		}
		if _, duplicate := expected[id]; duplicate {
			return fmt.Errorf("expected receiver ID %q is duplicated", id)
		}
		expected[id] = struct{}{}
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
	if len(status.Receivers) != len(expected) {
		return fmt.Errorf("receiver count is %d, want %d", len(status.Receivers), len(expected))
	}
	for id, receiver := range status.Receivers {
		if _, ok := expected[id]; !ok {
			return fmt.Errorf("unexpected receiver %q", id)
		}
		if receiver.State != "connected" {
			return fmt.Errorf("receiver %q state is %q", id, receiver.State)
		}
	}
	return nil
}

func readLocalBaseURL(reader io.Reader) (string, error) {
	var document struct {
		Server struct {
			ListenAddr string `yaml:"listen_addr"`
		} `yaml:"server"`
	}
	decoder := yaml.NewDecoder(io.LimitReader(reader, (1<<20)+1))
	decoder.KnownFields(false)
	if err := decoder.Decode(&document); err != nil {
		return "", fmt.Errorf("decode config: %w", err)
	}
	host, portText, err := net.SplitHostPort(document.Server.ListenAddr)
	if err != nil {
		return "", fmt.Errorf("server.listen_addr must contain a host and port: %w", err)
	}
	if host != "localhost" {
		address := net.ParseIP(host)
		if address == nil || !address.IsLoopback() {
			return "", fmt.Errorf("server.listen_addr host must be loopback")
		}
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return "", fmt.Errorf("server.listen_addr port must be between 1 and 65535")
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(port)), nil
}
