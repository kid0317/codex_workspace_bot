package output

import "strings"

type Phase string

const (
	Commentary  Phase = "commentary"
	FinalAnswer Phase = "final_answer"
)

type Item struct {
	ID, Type, Phase, Text string
}

type Presentation struct {
	ID    string
	Phase Phase
	Text  string
}

type Mapper struct{ seen map[string]struct{} }

func NewMapper() *Mapper { return &Mapper{seen: make(map[string]struct{})} }

func (m *Mapper) Accept(item Item) (Presentation, bool) {
	if item.ID == "" || item.Type != "agentMessage" || item.Text == "" {
		return Presentation{}, false
	}
	phase := Phase(item.Phase)
	if phase != Commentary && phase != FinalAnswer {
		return Presentation{}, false
	}
	if _, exists := m.seen[item.ID]; exists {
		return Presentation{}, false
	}
	m.seen[item.ID] = struct{}{}
	return Presentation{ID: item.ID, Phase: phase, Text: Sanitize(item.Text)}, true
}

func Sanitize(text string) string {
	text = strings.ReplaceAll(text, "&", "&amp;")
	text = strings.ReplaceAll(text, "<", "&lt;")
	return strings.ReplaceAll(text, ">", "&gt;")
}

type Projection struct {
	progress []string
	final    string
}

func NewProjection() *Projection { return &Projection{} }

func (p *Projection) Apply(item Presentation) {
	switch item.Phase {
	case Commentary:
		p.progress = append(p.progress, item.Text)
	case FinalAnswer:
		if item.Text != "" {
			p.final = item.Text
		}
	}
}

func (p *Projection) Progress() string { return strings.Join(p.progress, "\n\n") }
func (p *Projection) Final() string    { return p.final }
