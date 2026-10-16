package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

type fixture struct {
	Step    string `json:"step"`
	DocType string `json:"doc_type"`
	System  string `json:"system"`
	User    string `json:"user"`
}

func loadFixtures(t *testing.T) []fixture {
	t.Helper()
	data, err := os.ReadFile("testdata/prompts.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx []fixture
	if err := json.Unmarshal(data, &fx); err != nil {
		t.Fatal(err)
	}
	if len(fx) == 0 {
		t.Fatal("no fixtures")
	}
	return fx
}

var fixedNow = func() time.Time { return time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC) }

func testReplier(types ...WeightedType) replier {
	if len(types) == 0 {
		types = []WeightedType{{Name: "durc", Weight: 1}}
	}
	return replier{docTypes: types, rand: NewRand(1), now: fixedNow}
}

func TestDetectStepOnPipelinePrompts(t *testing.T) {
	for _, f := range loadFixtures(t) {
		got := detectStep(prompt{System: f.System, User: f.User})
		if got != f.Step {
			t.Errorf("%s/%s: detected %q", f.Step, f.DocType, got)
		}
	}
}

func TestDetectStepUnknown(t *testing.T) {
	if got := detectStep(prompt{System: "You are a helpful assistant."}); got != stepUnknown {
		t.Fatalf("got %q", got)
	}
	if _, err := testReplier().reply(stepUnknown, prompt{}, ""); err == nil {
		t.Fatal("unknown step must be refused")
	}
}

// Every field of every extraction schema is filled with a value of its type.
func TestExtractReplyFillsEverySchemaField(t *testing.T) {
	r := testReplier()
	n := 0
	for _, f := range loadFixtures(t) {
		if f.Step != stepExtract {
			continue
		}
		n++
		content, err := r.reply(stepExtract, prompt{System: f.System}, "")
		if err != nil {
			t.Fatalf("%s: %v", f.DocType, err)
		}
		var got map[string]any
		if err := json.Unmarshal([]byte(content), &got); err != nil {
			t.Fatalf("%s: reply is not a JSON object: %v", f.DocType, err)
		}
		var schema map[string]any
		if err := jsonAfter(f.System, "in JSON format:", &schema); err != nil {
			t.Fatal(err)
		}
		props := schema["properties"].(map[string]any)
		if len(got) != len(props) {
			t.Errorf("%s: %d fields, schema has %d", f.DocType, len(got), len(props))
		}
		for name := range props {
			if got[name] == nil {
				t.Errorf("%s: field %q is null", f.DocType, name)
			}
		}
	}
	if n < 40 {
		t.Fatalf("only %d extract fixtures", n)
	}
}

func TestExtractDates(t *testing.T) {
	schema := `in JSON format:
{"properties": {"data_rilascio": {"anyOf": [{"type": "string"}, {"type": "null"}]},
 "scadenza": {"anyOf": [{"type": "string"}, {"type": "null"}]},
 "nome": {"type": "string"}, "ore": {"type": "integer"},
 "voci": {"type": "array", "items": {"$ref": "#/$defs/Voce"}}},
 "$defs": {"Voce": {"properties": {"x": {"type": "boolean"}}, "type": "object"}}}
Respond ONLY with the JSON.`
	content, err := testReplier().extract(prompt{System: schema})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"data_rilascio":"2026-09-15","nome":"mock nome","ore":1,"scadenza":"2027-10-15","voci":[{"x":true}]}`
	if content != want {
		t.Fatalf("got  %s\nwant %s", content, want)
	}
}

func TestExtractWithoutSchemaFails(t *testing.T) {
	if _, err := testReplier().extract(prompt{System: "You are an expert in extracting data from x"}); err == nil {
		t.Fatal("want error")
	}
}

func TestLegalAnswersEveryChecklistElement(t *testing.T) {
	for _, f := range loadFixtures(t) {
		if f.Step != stepLegal {
			continue
		}
		var checklist []struct {
			Elemento string `json:"elemento"`
		}
		if err := jsonAfter(f.System, "(checklist fissa):", &checklist); err != nil {
			t.Fatal(err)
		}
		content, err := testReplier().reply(stepLegal, prompt{System: f.System}, "")
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Elementi []map[string]any `json:"elementi"`
		}
		if err := json.Unmarshal([]byte(content), &got); err != nil {
			t.Fatal(err)
		}
		if len(got.Elementi) != len(checklist) {
			t.Fatalf("%s: %d elementi for %d checklist rows", f.DocType, len(got.Elementi), len(checklist))
		}
		for i, e := range got.Elementi {
			if e["elemento"] != checklist[i].Elemento || e["esito"] != "conforme" {
				t.Errorf("%s: element %d = %v", f.DocType, i, e)
			}
		}
	}
}

func TestRulesTargetIsAnAllowedDocType(t *testing.T) {
	for _, f := range loadFixtures(t) {
		if f.Step != stepRules {
			continue
		}
		content, err := testReplier().reply(stepRules, prompt{System: f.System}, "")
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Regole []map[string]string `json:"regole"`
		}
		if err := json.Unmarshal([]byte(content), &got); err != nil {
			t.Fatal(err)
		}
		_, allowed, _ := strings.Cut(f.System, "valori esatti:")
		for _, rule := range got.Regole {
			if !strings.Contains(", "+strings.TrimSpace(allowed)+",", ", "+rule["target_doc_type"]+",") {
				t.Errorf("target_doc_type %q not in the prompt's list", rule["target_doc_type"])
			}
		}
	}
}

func TestSplitIsOneDocumentFromPageOne(t *testing.T) {
	pdf := []byte("%PDF-1.4\n1 0 obj <</Type /Pages /Count 3>> endobj\n" +
		"2 0 obj <</Type /Page>> endobj 3 0 obj <</Type/Page /Parent 1 0 R>> endobj 4 0 obj <</Type /Page>> endobj")
	content, err := testReplier().split(prompt{PDF: pdf, PDFPages: countPDFPages(pdf)})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Documenti []map[string]any `json:"documenti"`
	}
	if err := json.Unmarshal([]byte(content), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Documenti) != 1 {
		t.Fatalf("got %d documents", len(got.Documenti))
	}
	d := got.Documenti[0]
	if d["pagina_inizio"] != 1.0 || d["pagina_fine"] != 3.0 || d["ruolo"] != "documento" {
		t.Fatalf("got %v", d)
	}
}

func TestClassifyChoice(t *testing.T) {
	r := testReplier(WeightedType{"durc", 1})
	if got, _ := r.classify(prompt{}, "unilav"); got != "unilav" {
		t.Errorf("header: got %q", got)
	}
	if _, err := r.classify(prompt{}, "Bad Type"); err == nil {
		t.Error("bad header type must be refused")
	}
	if got, _ := r.classify(prompt{Filename: "mocktype-psc-0007.pdf"}, ""); got != "psc" {
		t.Errorf("filename: got %q", got)
	}
	if got, _ := r.classify(prompt{Filename: "scan.pdf"}, ""); got != "durc" {
		t.Errorf("default: got %q", got)
	}

	mix := testReplier(WeightedType{"durc", 3}, WeightedType{"unilav", 1})
	counts := map[string]int{}
	for range 4000 {
		got, _ := mix.classify(prompt{}, "")
		counts[got]++
	}
	if share := float64(counts["durc"]) / 4000; share < 0.70 || share > 0.80 {
		t.Errorf("durc share %.3f, want ~0.75 (%v)", share, counts)
	}
}

func TestOneWordSteps(t *testing.T) {
	r := testReplier()
	for step, want := range map[string]string{
		stepSecondOpinion: "nomina", stepShortPlan: "piano", stepIDExpiry: "NONE", stepEntityResolver: "NONE",
	} {
		if got, _ := r.reply(step, prompt{}, ""); got != want {
			t.Errorf("%s: got %q want %q", step, got, want)
		}
	}
}
