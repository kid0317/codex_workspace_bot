package attachment

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/kid0317/codex-workspace-bot/internal/storage"
)

var (
	ErrTooLarge = errors.New("attachment exceeds configured size limit")
	ErrInvalid  = errors.New("attachment is invalid")
)

const maxAttachmentFilenameBytes = 255

type Downloader interface {
	Download(context.Context, string, string, storage.AttachmentKind) (io.ReadCloser, string, error)
}

type Processor struct {
	Downloader   Downloader
	MaxFileBytes int64
}

type Input struct {
	WorkspaceDir, RootDir, AppID, ChannelKey, SessionID, AttachmentID string
	Kind                                                              storage.AttachmentKind
	SourceMessageID, ResourceKey, OriginalName                        string
}

type Result struct {
	RelativePath, ObservedMIME, SHA256, DisplayName string
	ByteSize                                        int64
}

// Materialize downloads exactly one resource on its owning channel worker,
// streams it through the configured limit, and atomically publishes payload.
func (p Processor) Materialize(ctx context.Context, input Input) (Result, error) {
	if p.Downloader == nil || p.MaxFileBytes <= 0 || input.WorkspaceDir == "" || strings.TrimSpace(input.RootDir) == "" || input.AppID == "" || input.ChannelKey == "" || input.SessionID == "" || input.AttachmentID == "" || input.SourceMessageID == "" || input.ResourceKey == "" {
		return Result{}, ErrInvalid
	}
	if input.Kind != storage.AttachmentImage && input.Kind != storage.AttachmentFile {
		return Result{}, ErrInvalid
	}
	body, _, err := p.Downloader.Download(ctx, input.SourceMessageID, input.ResourceKey, input.Kind)
	if err != nil {
		return Result{}, fmt.Errorf("download attachment: %w", err)
	}
	defer body.Close()
	dir := filepath.Join(attachmentRoot(input.WorkspaceDir, input.RootDir), pathHash(input.AppID), pathHash(input.ChannelKey), input.SessionID, input.AttachmentID)
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return Result{}, fmt.Errorf("create attachment directory: %w", err)
	}
	name := safeDisplayName(input.OriginalName)
	part := filepath.Join(dir, temporaryAttachmentLeaf(input.AttachmentID))
	payload := filepath.Join(dir, name)
	_ = os.Remove(part)
	defer os.Remove(part)
	file, err := os.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
	if err != nil {
		return Result{}, fmt.Errorf("create attachment part: %w", err)
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(file, hash), io.LimitReader(body, p.MaxFileBytes+1))
	closeErr := file.Close()
	if copyErr != nil {
		return Result{}, fmt.Errorf("stream attachment: %w", copyErr)
	}
	if closeErr != nil {
		return Result{}, fmt.Errorf("close attachment part: %w", closeErr)
	}
	if written == 0 {
		return Result{}, ErrInvalid
	}
	if written > p.MaxFileBytes {
		return Result{}, ErrTooLarge
	}
	if input.Kind == storage.AttachmentImage {
		opened, err := os.Open(part)
		if err != nil {
			return Result{}, fmt.Errorf("open image attachment: %w", err)
		}
		_, format, decodeErr := image.DecodeConfig(opened)
		_ = opened.Close()
		if decodeErr != nil {
			return Result{}, fmt.Errorf("%w: image decode", ErrInvalid)
		}
		if format == "jpeg" {
			format = "jpeg"
		}
	}
	observed, err := observedMIME(part)
	if err != nil {
		return Result{}, err
	}
	if err := os.Rename(part, payload); err != nil {
		return Result{}, fmt.Errorf("publish attachment payload: %w", err)
	}
	relative, err := filepath.Rel(input.WorkspaceDir, payload)
	if err != nil || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || relative == ".." {
		relative = payload
	}
	return Result{RelativePath: relative, ObservedMIME: observed, SHA256: hex.EncodeToString(hash.Sum(nil)), ByteSize: written, DisplayName: name}, nil
}

func attachmentRoot(workspaceDir, rootDir string) string {
	if filepath.IsAbs(rootDir) {
		return filepath.Clean(rootDir)
	}
	return filepath.Clean(filepath.Join(workspaceDir, rootDir))
}

func localAttachmentPath(workspaceDir, storedPath string) string {
	if filepath.IsAbs(storedPath) {
		return filepath.Clean(storedPath)
	}
	return filepath.Clean(filepath.Join(workspaceDir, storedPath))
}

func pathHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:8])
}

func observedMIME(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open attachment for type detection: %w", err)
	}
	defer file.Close()
	buf := make([]byte, 512)
	n, err := file.Read(buf)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read attachment for type detection: %w", err)
	}
	return http.DetectContentType(buf[:n]), nil
}

func safeDisplayName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if r < 32 || r == '/' || r == '\\' {
			return -1
		}
		return r
	}, name)
	if name == "" || name == "." || name == ".." {
		return "attachment"
	}
	if len(name) <= maxAttachmentFilenameBytes {
		return name
	}
	extension := filepath.Ext(name)
	if extension != "" && len(extension) < maxAttachmentFilenameBytes {
		base := truncateUTF8Bytes(strings.TrimSuffix(name, extension), maxAttachmentFilenameBytes-len(extension))
		if base != "" {
			return base + extension
		}
	}
	return truncateUTF8Bytes(name, maxAttachmentFilenameBytes)
}

func temporaryAttachmentLeaf(attachmentID string) string {
	return ".attachment-" + pathHash(attachmentID) + ".part"
}

func truncateUTF8Bytes(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	var builder strings.Builder
	builder.Grow(limit)
	for _, r := range value {
		size := utf8.RuneLen(r)
		if builder.Len()+size > limit {
			break
		}
		builder.WriteRune(r)
	}
	return builder.String()
}
