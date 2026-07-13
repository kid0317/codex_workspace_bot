package output

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

type LexedCompanion struct {
	SegmenterInput string
	StorageText    string
	Delimiter      string
}

func LexCompanion(text string) (LexedCompanion, error) {
	delimiter := "\x1ecompanion-send:" + uuid.NewString() + "\x1f"
	var segmenter, storage strings.Builder
	inFence := false
	fence := ""
	for i := 0; i < len(text); {
		if atLineStart(text, i) && (strings.HasPrefix(text[i:], "```") || strings.HasPrefix(text[i:], "~~~")) {
			marker := text[i : i+3]
			if !inFence {
				inFence, fence = true, marker
			} else if marker == fence {
				inFence, fence = false, ""
			}
			segmenter.WriteString(marker)
			storage.WriteString(marker)
			i += 3
			continue
		}
		if !inFence {
			if end, ok := singleMarkerLineEnd(text, i); ok {
				segmenter.WriteString(delimiter)
				segmenter.WriteByte('\n')
				i = end
				continue
			}
			if end, ok := doubleMarkerEnd(text, i); ok {
				if oddEscapes(text, i) {
					removeLastSlash(&segmenter)
					removeLastSlash(&storage)
					segmenter.WriteString(text[i:end])
					storage.WriteString(text[i:end])
				} else {
					segmenter.WriteString(delimiter)
					storage.WriteByte('\n')
				}
				i = end
				continue
			}
		}
		r, size := decodeRune(text[i:])
		segmenter.WriteRune(r)
		storage.WriteRune(r)
		i += size
	}
	return LexedCompanion{SegmenterInput: segmenter.String(), StorageText: storage.String(), Delimiter: delimiter}, nil
}

func SplitCompanion(input, delimiter string) []string {
	input = strings.TrimSpace(input)
	if input == "" {
		return []string{}
	}
	if strings.Contains(input, delimiter) {
		return splitAndBound(strings.Split(input, delimiter))
	}

	parts := splitParagraphs(input)
	if len(parts) == 1 && utf8.RuneCountInString(input) > 80 {
		parts = splitSentences(input)
	}
	segments := splitAndBound(parts)
	if len(segments) > 3 {
		return []string{input}
	}
	return segments
}

func atLineStart(text string, i int) bool { return i == 0 || text[i-1] == '\n' }

func oddEscapes(text string, i int) bool {
	count := 0
	for j := i - 1; j >= 0 && text[j] == '\\'; j-- {
		count++
	}
	return count%2 == 1
}

func removeLastSlash(builder *strings.Builder) {
	value := builder.String()
	if strings.HasSuffix(value, "\\") {
		builder.Reset()
		builder.WriteString(value[:len(value)-1])
	}
}

func doubleMarkerEnd(text string, start int) (int, bool) {
	if !isOpenBracketAt(text, start) {
		return 0, false
	}
	for end := start; end < len(text) && end-start <= 80; {
		_, size := decodeRune(text[end:])
		if size == 0 || end+size > len(text) {
			break
		}
		end += size
		candidate := normalizeCandidate(text[start:end])
		if candidate == "[[SEND]]" {
			return end, true
		}
	}
	return 0, false
}

// singleMarkerLineEnd accepts a malformed single-bracket marker only when it
// occupies a complete control line. This avoids splitting normal prose that
// happens to mention [SEND]. The optional quote/list wrapper is intentionally
// discarded with the marker.
func singleMarkerLineEnd(text string, start int) (int, bool) {
	if !atLineStart(text, start) {
		return 0, false
	}
	end := strings.IndexByte(text[start:], '\n')
	if end < 0 {
		end = len(text)
	} else {
		end += start
	}
	line := strings.TrimSpace(text[start:end])
	for {
		switch {
		case strings.HasPrefix(line, ">"):
			line = strings.TrimSpace(strings.TrimPrefix(line, ">"))
		case len(line) >= 2 && (line[0] == '-' || line[0] == '*' || line[0] == '+') && (line[1] == ' ' || line[1] == '\t'):
			line = strings.TrimSpace(line[2:])
		default:
			if normalizeCandidate(line) == "[SEND]" {
				if end < len(text) {
					return end + 1, true
				}
				return end, true
			}
			return 0, false
		}
	}
}

func splitAndBound(parts []string) []string {
	segments := make([]string, 0, len(parts))
	for _, part := range parts {
		if text := strings.TrimSpace(part); text != "" {
			segments = append(segments, hardSplit(text, 80)...)
		}
	}
	return mergeShortSegments(segments, 2, 80)
}

func splitParagraphs(text string) []string {
	var parts []string
	for remaining := text; ; {
		index := strings.Index(remaining, "\n\n")
		if index < 0 {
			return append(parts, remaining)
		}
		parts = append(parts, remaining[:index])
		remaining = strings.TrimLeft(remaining[index:], "\n")
	}
}

func splitSentences(text string) []string {
	var result []string
	var current strings.Builder
	runes := []rune(text)
	for i, r := range runes {
		current.WriteRune(r)
		if r == '。' || r == '！' || r == '？' || r == '!' || r == '?' || (r == '.' && (i+1 == len(runes) || runes[i+1] == ' ')) {
			result = append(result, current.String())
			current.Reset()
		}
	}
	if current.Len() > 0 {
		result = append(result, current.String())
	}
	return result
}

func hardSplit(text string, maxRunes int) []string {
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return []string{text}
	}
	segments := make([]string, 0, len(runes)/maxRunes+1)
	for len(runes) > maxRunes {
		cut := maxRunes
		for i := maxRunes - 1; i >= maxRunes/2; i-- {
			if strings.ContainsRune("。！？!?.,，、;； ", runes[i]) {
				cut = i + 1
				break
			}
		}
		segments = append(segments, strings.TrimSpace(string(runes[:cut])))
		runes = runes[cut:]
	}
	if tail := strings.TrimSpace(string(runes)); tail != "" {
		segments = append(segments, tail)
	}
	return segments
}

func mergeShortSegments(segments []string, minRunes, maxRunes int) []string {
	result := make([]string, 0, len(segments))
	for _, segment := range segments {
		if len(result) > 0 && utf8.RuneCountInString(segment) < minRunes {
			merged := result[len(result)-1] + " " + segment
			if utf8.RuneCountInString(merged) <= maxRunes {
				result[len(result)-1] = merged
				continue
			}
		}
		result = append(result, segment)
	}
	return result
}

func isOpenBracketAt(text string, i int) bool {
	r, _ := decodeRune(text[i:])
	return r == '[' || r == '［' || r == '【' || r == '〔'
}

func normalizeCandidate(value string) string {
	replacer := strings.NewReplacer("［", "[", "］", "]", "【", "[", "】", "]", "〔", "[", "〕", "]")
	value = replacer.Replace(value)
	var out strings.Builder
	for _, r := range value {
		if unicode.IsSpace(r) || r == '\u200b' || r == '\u200c' || r == '\u200d' || r == '\ufeff' {
			continue
		}
		out.WriteRune(unicode.ToUpper(r))
	}
	return out.String()
}

func decodeRune(text string) (rune, int) {
	if text == "" {
		return 0, 0
	}
	return utf8.DecodeRuneInString(text)
}
