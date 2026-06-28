package output

import (
	"context"
	"errors"
	"strings"
)

type Filter interface {
	Filter(ctx context.Context, text string) (string, error)
}

type FilterFunc func(ctx context.Context, text string) (string, error)

func (f FilterFunc) Filter(ctx context.Context, text string) (string, error) {
	return f(ctx, text)
}

type Result struct {
	Segments   []string
	StoredText string
}

func Process(ctx context.Context, text string, filter Filter) (Result, error) {
	if filter != nil {
		next, err := filter.Filter(ctx, text)
		if err == nil && next != "" {
			text = next
		}
	}
	segments := SplitSegments(text)
	if len(segments) == 0 {
		return Result{}, errors.New("最终输出为空")
	}
	return Result{Segments: segments, StoredText: strings.Join(segments, "\n")}, nil
}

func SplitSegments(text string) []string {
	raw := strings.Split(text, "[[SEND]]")
	out := make([]string, 0, len(raw))
	for _, part := range raw {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
