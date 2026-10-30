package mcp_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akopichin/afm/pkg/jsonstrict"
	"github.com/akopichin/afm/pkg/mcp"
)

func TestQuestionDecodeRefusesAmbiguity(t *testing.T) {
	for _, raw := range []string{
		`{"question":"first","question":"second"}`,
		`{"question":"first"} {"question":"second"}`,
		`{"question":"cut off`,
		`{"question":"cut off}`,
		`[{"question":"wrapped"}]`,
		`{"question":"ok","unknown":"keep"}`,
		`{"question":"ok","options":[42]}`,
		strings.Repeat(" ", 256*1024) + `{"question":"ok"}`,
	} {
		if mcp.CanParseQuestion([]byte(raw)) {
			t.Errorf("accepted unsafe question (%d bytes): %.100s", len(raw), raw)
		}
	}
}

func TestQuestionRecoveryPreservesRawAndDefaults(t *testing.T) {
	raw := []byte(`{"id":"payload-id","question":"Proceed?",options":["Yes","No"],"allow_custom":null}`)
	before := bytes.Clone(raw)
	q, fixed, kind, err := mcp.RecoverQuestion(raw)
	if err != nil || kind != jsonstrict.MechanicalRepair || q.AllowCustom != nil {
		t.Fatalf("q=%+v kind=%s err=%v", q, kind, err)
	}
	if _, err := mcp.DecodeQuestion(fixed); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, before) {
		t.Fatal("raw mutated")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "planning.q1.question.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	questions, err := mcp.FindUnansweredQuestions(dir)
	if err != nil || len(questions) != 1 {
		t.Fatalf("questions=%+v err=%v", questions, err)
	}
	if questions[0].ID != "q1" || !questions[0].AllowCustom || questions[0].Malformed {
		t.Fatalf("filename/defaults lost: %+v", questions[0])
	}
	saved, err := os.ReadFile(path + ".raw")
	if err != nil || !bytes.Equal(saved, raw) {
		t.Fatalf("original raw not preserved: %v", err)
	}
}

func TestQuestionRecoveryReusedIDPreservesBothRawPayloads(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "planning.q1.question.json")
	raws := [][]byte{[]byte(`{"question":"First?",}`), []byte(`{"question":"Second?",}`)}
	for _, raw := range raws {
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := mcp.FindUnansweredQuestions(dir); err != nil {
			t.Fatal(err)
		}
	}
	paths, err := filepath.Glob(path + "*.raw")
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range raws {
		found := false
		for _, snapshot := range paths {
			data, err := os.ReadFile(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			found = found || bytes.Equal(data, raw)
		}
		if !found {
			t.Fatalf("lost original payload %s", raw)
		}
	}
}

func TestQuestionLocalRepairReceipt(t *testing.T) {
	dir := t.TempDir()
	raw := []byte(`{"id":"q1","question":"Choose","options":["A"],}`)
	path := filepath.Join(dir, "planning.q1.question.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	questions, err := mcp.FindUnansweredQuestions(dir)
	if err != nil || len(questions) != 1 || questions[0].Malformed {
		t.Fatalf("questions=%+v err=%v", questions, err)
	}
	paths, err := filepath.Glob(path + ".*.recovery.json")
	if err != nil || len(paths) != 1 {
		t.Fatalf("receipt paths=%v err=%v", paths, err)
	}
	data, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	var receipt map[string]any
	if err := json.Unmarshal(data, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt["contract"] != "question" || receipt["source"] != "mechanical" || receipt["validation"] != "accepted" || receipt["owner"] != "question/planning.q1.question.json" {
		t.Fatalf("receipt=%s", data)
	}
	if receipt["raw_sha256"] != fmt.Sprintf("%x", sha256.Sum256(raw)) {
		t.Fatal("wrong source identity")
	}
	fixed, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if receipt["candidate_sha256"] != fmt.Sprintf("%x", sha256.Sum256(fixed)) {
		t.Fatal("wrong candidate identity")
	}
	original, err := os.ReadFile(path + ".raw")
	if err != nil || !bytes.Equal(raw, original) {
		t.Fatal("original raw was not preserved")
	}
	if strings.Contains(string(data), "Choose") {
		t.Fatal("receipt contains raw content")
	}
}
