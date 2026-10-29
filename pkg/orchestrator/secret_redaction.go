package orchestrator

import (
	"github.com/akopichin/afm/pkg/assistants"
	"github.com/akopichin/afm/pkg/executor"
	"github.com/akopichin/afm/pkg/redact"
)

// secretValues includes transport/environment credentials, resolved secrets.env values and live mesh tokens.
// Tokens live only in child descriptors, so environment-only redaction would miss them.
func (o *Orchestrator) secretValues() []string {
	vals := append(executor.SecretValuesForRedaction(), o.opts.SecretValues...)
	return append(vals, o.meshTokens().values()...)
}

// RedactSecrets prevents secrets in mesh input and forwarded text from reaching durable journals,
// recipients, notices or logs verbatim.
func (o *Orchestrator) RedactSecrets(s string) string {
	return redact.String(s, o.secretValues())
}

// redactAll is RedactSecrets for every element of ss (a list of artifact references, evidence lines, ...).
func (o *Orchestrator) redactAll(ss []string) []string {
	if ss == nil {
		return nil
	}
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = o.RedactSecrets(s)
	}
	return out
}

// sameRedactedList is sameRedactedText for lists: the same length and the same text at every position.
func (o *Orchestrator) sameRedactedList(stored, requested []string) bool {
	if len(stored) != len(requested) {
		return false
	}
	for i := range stored {
		if !o.sameRedactedText(stored[i], requested[i]) {
			return false
		}
	}
	return true
}

// sameRedactedText applies current secrets to both sides of a retried operation, so newly minted tokens
// cannot turn a faithful retry into a conflict.
func (o *Orchestrator) sameRedactedText(stored, requested string) bool {
	return o.RedactSecrets(stored) == o.RedactSecrets(requested)
}

// redactSet is the redacted text, or nil when it is not set.
func (o *Orchestrator) redactSet(text *string) *string {
	if text == nil {
		return nil
	}
	return new(o.RedactSecrets(*text))
}

// redactRefs is the redacted artifact refs, or nil when they are not set; a set empty list stays an empty list.
func (o *Orchestrator) redactRefs(refs *[]string) *[]string {
	if refs == nil {
		return nil
	}
	out := make([]string, len(*refs))
	for i, ref := range *refs {
		out[i] = o.RedactSecrets(ref)
	}
	return &out
}

// redactProfile masks secrets in the human-readable text of a profile before it is frozen into a task's
// journaled snapshot: its title and instructions. The command is left as configured — it is what runs.
func (o *Orchestrator) redactProfile(p assistants.Profile) assistants.Profile {
	p.Title, p.Instructions = o.RedactSecrets(p.Title), o.RedactSecrets(p.Instructions)
	return p
}

// redactResult masks secrets in every free-text field of a helper's result before it is written to result.json
// or the journal's task_result: summary, findings, evidence, suggested change and limitations.
func (o *Orchestrator) redactResult(r assistants.Result) assistants.Result {
	r.Summary = o.RedactSecrets(r.Summary)
	r.Evidence = o.redactAll(r.Evidence)
	r.SuggestedChange = o.RedactSecrets(r.SuggestedChange)
	r.Limitations = o.RedactSecrets(r.Limitations)
	findings := make([]assistants.Finding, len(r.Findings))
	for i, f := range r.Findings {
		f.ID, f.Severity, f.Detail = o.RedactSecrets(f.ID), o.RedactSecrets(f.Severity), o.RedactSecrets(f.Detail)
		if f.Path != nil {
			path := o.RedactSecrets(*f.Path)
			f.Path = &path
		}
		findings[i] = f
	}
	if r.Findings != nil {
		r.Findings = findings
	}
	return r
}
