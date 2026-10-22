package assistants

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestParseCollaborationMode(t *testing.T) {
	tests := []struct {
		in      string
		want    CollaborationMode
		wantErr bool
	}{
		{in: "team", want: CollaborationModeTeam},
		{in: "assistants", want: CollaborationModeAssistants},
		{in: "", wantErr: true},
		{in: "Team", wantErr: true},
		{in: " team", wantErr: true},
		{in: "unknown", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseCollaborationMode(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
			if err != nil && !strings.Contains(err.Error(), "collaboration.mode") {
				t.Errorf("error %q must name collaboration.mode", err)
			}
		})
	}
}

// TestParseCollaborationModeNode проверяет сырой YAML-узел: null, число, bool и mapping не должны
// превращаться в строку или пропадать как «поле не задано».
func TestParseCollaborationModeNode(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		want    CollaborationMode
		wantErr bool
	}{
		{name: "team", yaml: "mode: team", want: CollaborationModeTeam},
		{name: "assistants", yaml: "mode: assistants", want: CollaborationModeAssistants},
		{name: "quoted", yaml: `mode: "assistants"`, want: CollaborationModeAssistants},
		{name: "empty string", yaml: `mode: ""`, wantErr: true},
		{name: "explicit null", yaml: "mode: null", wantErr: true},
		{name: "tilde null", yaml: "mode: ~", wantErr: true},
		{name: "bare key", yaml: "mode:", wantErr: true},
		{name: "number", yaml: "mode: 1", wantErr: true},
		{name: "bool", yaml: "mode: true", wantErr: true},
		{name: "mapping", yaml: "mode: {a: b}", wantErr: true},
		{name: "sequence", yaml: "mode: [team]", wantErr: true},
		{name: "unknown", yaml: "mode: solo", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var doc yaml.Node
			if err := yaml.Unmarshal([]byte(tt.yaml), &doc); err != nil {
				t.Fatal(err)
			}
			value := doc.Content[0].Content[1] // значение единственного ключа mode
			got, err := ParseCollaborationModeNode(value)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
			if err != nil && !strings.Contains(err.Error(), "collaboration.mode") {
				t.Errorf("error %q must name collaboration.mode", err)
			}
		})
	}
}

// TestParseCollaborationModeNode_Alias: алиас разворачивается до целевого узла — алиас на корректную
// строку принимается, алиас на null/число/mapping отвергается так же, как сам узел.
func TestParseCollaborationModeNode_Alias(t *testing.T) {
	tests := []struct {
		name    string
		anchor  string // значение с якорем &m
		want    CollaborationMode
		wantErr bool
	}{
		{name: "valid string", anchor: "assistants", want: CollaborationModeAssistants},
		{name: "unknown string", anchor: "solo", wantErr: true},
		{name: "null", anchor: "null", wantErr: true},
		{name: "number", anchor: "1", wantErr: true},
		{name: "mapping", anchor: "{a: b}", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var doc yaml.Node
			if err := yaml.Unmarshal([]byte("src: &m "+tt.anchor+"\nmode: *m\n"), &doc); err != nil {
				t.Fatal(err)
			}
			value := doc.Content[0].Content[3] // значение второго ключа (mode)
			if value.Kind != yaml.AliasNode {
				t.Fatalf("test setup: Kind = %v, want AliasNode", value.Kind)
			}
			got, err := ParseCollaborationModeNode(value)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
			if err != nil && !strings.Contains(err.Error(), "collaboration.mode") {
				t.Errorf("error %q must name collaboration.mode", err)
			}
		})
	}
}
