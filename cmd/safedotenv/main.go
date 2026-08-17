package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"syscall"
	"unicode/utf8"
)

const maxDotenvBytes = 1 << 20

var exactAllowedKeys = map[string]struct{}{
	"CODEX_HOME":               {},
	"USER_DIR":                 {},
	"AIPM_MOUNT_WORKSPACE_DIR": {},
	"AIPM_MOUNT_USER_DIR":      {},
	"AIPM_STATE":               {},
	"SANDBOX_STATE":            {},
}

var allowedKeyPrefixes = []string{
	"CODEX_WORKSPACE_BOT_",
	"OPENAI_",
	"DASHSCOPE_",
	"LANGFUSE_",
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	command := os.Args[1]
	file, key, allowMissing, rest, err := parseArguments(os.Args[2:])
	if err != nil {
		fail(err)
	}
	if file == "" {
		fail(fmt.Errorf("--file is required"))
	}
	values, err := loadDotenv(file)
	if err != nil {
		fail(err)
	}

	switch command {
	case "validate":
		if len(rest) != 0 || key != "" || allowMissing {
			usage()
		}
	case "get":
		if key == "" || len(rest) != 0 {
			usage()
		}
		value, ok := values[key]
		if !ok && !allowMissing {
			fail(fmt.Errorf("key %s is missing", key))
		}
		if ok {
			fmt.Print(value)
		}
	case "exec":
		if key != "" || allowMissing || len(rest) == 0 {
			usage()
		}
		path, err := resolveExecutable(rest[0], childEnvironment(os.Environ(), values))
		if err != nil {
			fail(err)
		}
		if err := syscall.Exec(path, rest, childEnvironment(os.Environ(), values)); err != nil {
			fail(fmt.Errorf("exec %s: %w", rest[0], err))
		}
	default:
		usage()
	}
}

func parseArguments(arguments []string) (file, key string, allowMissing bool, rest []string, err error) {
	for len(arguments) > 0 {
		switch arguments[0] {
		case "--file":
			if len(arguments) < 2 || file != "" {
				return "", "", false, nil, fmt.Errorf("--file requires one value")
			}
			file = arguments[1]
			arguments = arguments[2:]
		case "--key":
			if len(arguments) < 2 || key != "" {
				return "", "", false, nil, fmt.Errorf("--key requires one value")
			}
			key = arguments[1]
			arguments = arguments[2:]
		case "--allow-missing":
			allowMissing = true
			arguments = arguments[1:]
		case "--":
			return file, key, allowMissing, arguments[1:], nil
		default:
			return file, key, allowMissing, arguments, nil
		}
	}
	return file, key, allowMissing, nil, nil
}

func loadDotenv(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open dotenv: %w", err)
	}
	defer file.Close()
	return parseDotenv(io.LimitReader(file, maxDotenvBytes+1))
}

func parseDotenv(reader io.Reader) (map[string]string, error) {
	limited, err := io.ReadAll(io.LimitReader(reader, maxDotenvBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read dotenv: %w", err)
	}
	if len(limited) > maxDotenvBytes {
		return nil, fmt.Errorf("dotenv exceeds 1 MiB")
	}

	values := make(map[string]string)
	scanner := bufio.NewScanner(strings.NewReader(string(limited)))
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := scanner.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, encoded, ok := strings.Cut(line, "=")
		if !ok || !validKeySyntax(name) || !allowedKey(name) {
			return nil, fmt.Errorf("dotenv line %d has an unsupported key or syntax", lineNumber)
		}
		if _, exists := values[name]; exists {
			return nil, fmt.Errorf("dotenv line %d duplicates %s", lineNumber, name)
		}
		value, err := decodeShellQuotedData(encoded)
		if err != nil {
			return nil, fmt.Errorf("dotenv line %d value is invalid: %w", lineNumber, err)
		}
		values[name] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan dotenv: %w", err)
	}
	return values, nil
}

func validKeySyntax(name string) bool {
	if name == "" || name[0] < 'A' || name[0] > 'Z' {
		return false
	}
	for _, character := range name[1:] {
		if (character < 'A' || character > 'Z') && (character < '0' || character > '9') && character != '_' {
			return false
		}
	}
	return true
}

func allowedKey(name string) bool {
	if _, ok := exactAllowedKeys[name]; ok {
		return true
	}
	for _, prefix := range allowedKeyPrefixes {
		if strings.HasPrefix(name, prefix) && len(name) > len(prefix) {
			return true
		}
	}
	return false
}

// decodeShellQuotedData supports the simple backslash escaping produced by
// Bash printf %q. It is deliberately not a shell parser: executable shell
// constructs and unescaped metacharacters are rejected instead of evaluated.
func decodeShellQuotedData(encoded string) (string, error) {
	if encoded == "''" {
		return "", nil
	}
	var decoded strings.Builder
	for index := 0; index < len(encoded); {
		if encoded[index] == '\\' {
			index++
			if index == len(encoded) {
				return "", fmt.Errorf("dangling backslash")
			}
			r, size := utf8.DecodeRuneInString(encoded[index:])
			if r == utf8.RuneError && size == 1 {
				return "", fmt.Errorf("invalid UTF-8")
			}
			if r < 0x20 || r == 0x7f {
				return "", fmt.Errorf("control character")
			}
			decoded.WriteRune(r)
			index += size
			continue
		}
		r, size := utf8.DecodeRuneInString(encoded[index:])
		if r == utf8.RuneError && size == 1 {
			return "", fmt.Errorf("invalid UTF-8")
		}
		if r < 0x20 || r == 0x7f || strings.ContainsRune("$`'\";|&<>(){}", r) {
			return "", fmt.Errorf("unescaped shell metacharacter")
		}
		decoded.WriteRune(r)
		index += size
	}
	return decoded.String(), nil
}

func childEnvironment(base []string, values map[string]string) []string {
	result := make(map[string]string)
	for _, item := range base {
		name, value, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		if name == "HOME" || name == "PATH" || name == "TMPDIR" || name == "LANG" || name == "TZ" ||
			name == "USER" || name == "LOGNAME" || name == "SSL_CERT_FILE" || strings.HasPrefix(name, "LC_") {
			result[name] = value
		}
	}
	for name, value := range values {
		result[name] = value
	}
	keys := make([]string, 0, len(result))
	for name := range result {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	environment := make([]string, 0, len(keys))
	for _, name := range keys {
		environment = append(environment, name+"="+result[name])
	}
	return environment
}

func resolveExecutable(name string, environment []string) (string, error) {
	if strings.ContainsRune(name, '/') {
		return name, nil
	}
	path := ""
	for _, item := range environment {
		if strings.HasPrefix(item, "PATH=") {
			path = strings.TrimPrefix(item, "PATH=")
			break
		}
	}
	for _, directory := range strings.Split(path, ":") {
		candidate := directory + "/" + name
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode()&0111 != 0 {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("executable %s was not found in PATH", name)
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: safedotenv validate --file FILE | get --file FILE --key KEY [--allow-missing] | exec --file FILE -- COMMAND [ARGS...]")
	os.Exit(2)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "safedotenv:", err)
	os.Exit(1)
}
