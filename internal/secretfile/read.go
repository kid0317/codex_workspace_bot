package secretfile

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

const maxSecretBytes = 64 * 1024

func Read(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("secret file: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > maxSecretBytes {
		return "", errors.New("secret file must be a regular file no larger than 64 KiB")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("secret file: %w", err)
	}
	value := strings.TrimSuffix(string(data), "\n")
	value = strings.TrimSuffix(value, "\r")
	if value == "" {
		return "", errors.New("secret file is empty")
	}
	if strings.ContainsAny(value, "\r\n\x00") {
		return "", errors.New("secret file must contain exactly one line")
	}
	return value, nil
}
