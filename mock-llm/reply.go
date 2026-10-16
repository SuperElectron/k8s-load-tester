package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Step names, also the "step" label of the metrics.
const (
	stepClassify       = "classify"
	stepSecondOpinion  = "classify_second_opinion"
	stepShortPlan      = "classify_short_plan"
	stepExtract        = "extract"
	stepIDExpiry       = "id_expiry"
	stepSplit          = "split"
	stepLegal          = "legal"
	stepRules          = "rules"
	stepEntityResolver = "entity_resolver"
	stepUnknown        = "unknown"
)

// stepMarkers map a phrase of a pipeline system prompt to the step that sent
// it. Checked in order; the first hit wins.
var stepMarkers = []struct{ marker, step string }{
	{`{"documenti": [{"pagina_inizio"`, stepSplit},
	{"Valuta ESATTAMENTE questi elementi", stepLegal},
	{`{"regole": [`, stepRules},
	{"You are an expert in classifying", stepClassify},
	{"'attestato' if it CERTIFIES", stepSecondOpinion},
	{"'accettazione' if it is the front page", stepShortPlan},
	{"You read ONE date on an identity document", stepIDExpiry},
	{"precise entity matcher", stepEntityResolver},
	{"You are an expert in extracting data from", stepExtract},
}

var (
	docTypeName   = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	filenameType  = regexp.MustCompile(`mocktype-([a-z][a-z0-9_]*)`)
	pdfPageObject = regexp.MustCompile(`/Type\s*/Page[^s]`)
)

// chatRequest is the part of an OpenAI chat/completions body the mock reads.
type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
}

type chatMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type contentPart struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	ImageURL *struct {
		URL string `json:"url"`
	} `json:"image_url"`
	File *struct {
		Filename string `json:"filename"`
		FileData string `json:"file_data"`
	} `json:"file"`
}

// prompt is a request flattened to what the reply depends on.
type prompt struct {
	System    string
	User      string
	Filename  string
	PDF       []byte
	PDFPages  int
	Images    int
	TextChars int
}

func parsePrompt(req chatRequest) (prompt, error) {
	var p prompt
	for i, m := range req.Messages {
		var text string
		var parts []contentPart
		switch {
		case len(m.Content) == 0 || string(m.Content) == "null":
		case m.Content[0] == '"':
			if err := json.Unmarshal(m.Content, &text); err != nil {
				return p, fmt.Errorf("messages[%d].content: %w", i, err)
			}
		default:
			if err := json.Unmarshal(m.Content, &parts); err != nil {
				return p, fmt.Errorf("messages[%d].content: %w", i, err)
			}
		}
		for _, part := range parts {
			switch {
			case part.Type == "text":
				text += part.Text
			case part.Type == "image_url" && part.ImageURL != nil:
				p.Images++
			case part.Type == "file" && part.File != nil:
				p.Filename = part.File.Filename
				data, err := decodeDataURI(part.File.FileData)
				if err != nil {
					return p, fmt.Errorf("messages[%d] file %q: %w", i, part.File.Filename, err)
				}
				p.PDF = data
				p.PDFPages = countPDFPages(data)
			}
		}
		p.TextChars += len(text)
		if m.Role == "system" {
			p.System += text
		} else {
			p.User += text
		}
	}
	return p, nil
}

func decodeDataURI(uri string) ([]byte, error) {
	_, payload, ok := strings.Cut(uri, ";base64,")
	if !ok {
		return nil, fmt.Errorf("file_data is not a base64 data URI")
	}
	return base64.StdEncoding.DecodeString(payload)
}

// countPDFPages counts page objects in the raw bytes; 0 when they sit in
// compressed object streams and cannot be seen without a PDF parser.
func countPDFPages(data []byte) int {
	return len(pdfPageObject.FindAll(data, -1))
}

// detectStep names the pipeline step from the system prompt, or from the
// whole text when there is no system message.
func detectStep(p prompt) string {
	text := p.System
	if text == "" {
		text = p.User
	}
	for _, m := range stepMarkers {
		if strings.Contains(text, m.marker) {
			return m.step
		}
	}
	return stepUnknown
}

// replier builds the canned content for each step.
type replier struct {
	docTypes []WeightedType
	rand     *Rand
	now      func() time.Time
}

// reply returns the assistant content for a step. forcedType is the
// X-Mock-Doc-Type header, empty when absent.
func (r replier) reply(step string, p prompt, forcedType string) (string, error) {
	switch step {
	case stepClassify:
		return r.classify(p, forcedType)
	case stepSecondOpinion:
		// Keeps the classifier's first verdict.
		return "nomina", nil
	case stepShortPlan:
		// Keeps the classifier's first verdict.
		return "piano", nil
	case stepIDExpiry, stepEntityResolver:
		// "Cannot read it" / "no match": the pipeline keeps what it has.
		return "NONE", nil
	case stepExtract:
		return r.extract(p)
	case stepSplit:
		return r.split(p)
	case stepLegal:
		return r.legal(p)
	case stepRules:
		return r.rules()
	}
	return "", fmt.Errorf("unrecognised prompt: no step marker in the system prompt")
}

func (r replier) classify(p prompt, forcedType string) (string, error) {
	if forcedType != "" {
		if !docTypeName.MatchString(forcedType) {
			return "", fmt.Errorf("X-Mock-Doc-Type %q is not a doc type name", forcedType)
		}
		return forcedType, nil
	}
	if m := filenameType.FindStringSubmatch(p.Filename); m != nil {
		return m[1], nil
	}
	var total float64
	for _, t := range r.docTypes {
		total += t.Weight
	}
	x := r.rand.Float64() * total
	for _, t := range r.docTypes {
		if x < t.Weight {
			return t.Name, nil
		}
		x -= t.Weight
	}
	return r.docTypes[len(r.docTypes)-1].Name, nil
}

// jsonAfter decodes the first JSON value that follows marker in text.
func jsonAfter(text, marker string, v any) error {
	i := strings.Index(text, marker)
	if i < 0 {
		return fmt.Errorf("marker %q not in prompt", marker)
	}
	rest := text[i+len(marker):]
	j := strings.IndexAny(rest, "{[")
	if j < 0 {
		return fmt.Errorf("no JSON after %q", marker)
	}
	return json.NewDecoder(strings.NewReader(rest[j:])).Decode(v)
}

func (r replier) extract(p prompt) (string, error) {
	var schema map[string]any
	if err := jsonAfter(p.System, "in JSON format:", &schema); err != nil {
		return "", fmt.Errorf("extract schema: %w", err)
	}
	v, err := instanceFor(schema, r.now())
	if err != nil {
		return "", fmt.Errorf("extract schema: %w", err)
	}
	return marshal(v)
}

func (r replier) split(p prompt) (string, error) {
	// One document from page 1: the pipeline rebuilds the end page from the
	// file itself, so the whole file stays one piece whatever the count says.
	last := max(p.PDFPages, 1)
	return marshal(map[string]any{"documenti": []any{map[string]any{
		"pagina_inizio": 1,
		"pagina_fine":   last,
		"descrizione":   "mock documento",
		"ruolo":         "documento",
		"accompagna":    nil,
		"data":          r.now().AddDate(0, -1, 0).Format(time.DateOnly),
	}}})
}

func (r replier) legal(p prompt) (string, error) {
	var checklist []struct {
		Elemento string `json:"elemento"`
	}
	if err := jsonAfter(p.System, "(checklist fissa):", &checklist); err != nil {
		return "", fmt.Errorf("legal checklist: %w", err)
	}
	if len(checklist) == 0 {
		return "", fmt.Errorf("legal checklist is empty")
	}
	elementi := make([]any, 0, len(checklist))
	for _, c := range checklist {
		elementi = append(elementi, map[string]any{
			"elemento":          c.Elemento,
			"esito":             "conforme",
			"motivazione":       "Contenuto presente nel documento (risposta mock).",
			"azione_correttiva": nil,
		})
	}
	return marshal(map[string]any{"elementi": elementi})
}

func (r replier) rules() (string, error) {
	return marshal(map[string]any{"regole": []any{map[string]any{
		"criterio":        "Il POS dell'impresa deve essere presente per il cantiere (regola mock).",
		"target_doc_type": "pos",
		"engine":          "deterministico",
		"severity":        "warning",
		"provenance":      "Lavorazioni",
	}}})
}

func marshal(v any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return strings.TrimSpace(buf.String()), nil
}

// Rough token counts for the usage block: ~4 characters per token, a flat
// cost per image and per PDF page (one page when the count is unknown).
const (
	tokensPerImage   = 258
	tokensPerPDFPage = 1500
)

func promptTokens(p prompt) int {
	n := (p.TextChars+3)/4 + p.Images*tokensPerImage
	if p.PDF != nil {
		n += max(p.PDFPages, 1) * tokensPerPDFPage
	}
	return n
}

func completionTokens(content string) int {
	return (len(content) + 3) / 4
}
