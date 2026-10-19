package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

// discussionQuestionsFile — questions the human asked a running stage agent from the stage discussion with
// "Interrupt agent" on. Kept apart from feedback.md: a question is not a revision of the agent's work.
const discussionQuestionsFile = "discussion-questions.json"

// DiscussionQuestion is one interrupting question: the discussion message to answer and its text.
type DiscussionQuestion struct {
	MessageID string `json:"message_id"`
	Body      string `json:"body"`
}

// SaveDiscussionQuestion atomically adds q to the stage's pending questions. Saving the same message id
// again is a no-op, so a retried delivery never asks twice.
func SaveDiscussionQuestion(stageDir string, q DiscussionQuestion) error {
	questions, err := readDiscussionQuestions(stageDir)
	if err != nil {
		return err
	}
	if slices.ContainsFunc(questions, func(x DiscussionQuestion) bool { return x.MessageID == q.MessageID }) {
		return nil
	}
	data, err := json.Marshal(append(questions, q))
	if err != nil {
		return fmt.Errorf("marshal discussion questions: %w", err)
	}
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		return fmt.Errorf("mkdir stage dir: %w", err)
	}
	return atomicWriteFile(filepath.Join(stageDir, discussionQuestionsFile), data, 0644)
}

// TakeDiscussionQuestions returns the stage's pending questions and removes them: each one goes into exactly
// one restarted prompt. The question also stays unread in the agent's mesh inbox until it answers, so a
// crash right after this call does not lose it.
func TakeDiscussionQuestions(stageDir string) ([]DiscussionQuestion, error) {
	questions, err := readDiscussionQuestions(stageDir)
	if err != nil || len(questions) == 0 {
		return nil, err
	}
	if err := os.Remove(filepath.Join(stageDir, discussionQuestionsFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("remove discussion questions: %w", err)
	}
	return questions, nil
}

func readDiscussionQuestions(stageDir string) ([]DiscussionQuestion, error) {
	data, err := os.ReadFile(filepath.Join(stageDir, discussionQuestionsFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read discussion questions: %w", err)
	}
	var questions []DiscussionQuestion
	if err := json.Unmarshal(data, &questions); err != nil {
		return nil, fmt.Errorf("decode discussion questions: %w", err)
	}
	return questions, nil
}
