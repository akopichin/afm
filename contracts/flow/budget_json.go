package flow

import (
	"encoding/json"

	"github.com/akopichin/afm/contracts/budget"
)

// UnmarshalJSON rejects a null or repeated Budget (see rejectBudgetJSON).
func (f *Flow) UnmarshalJSON(data []byte) error {
	if err := rejectBudgetJSON(data); err != nil {
		return err
	}
	type plain Flow // no methods: decoding it does not recurse into UnmarshalJSON
	return json.Unmarshal(data, (*plain)(f))
}

// UnmarshalJSON rejects a null or repeated Budget (see rejectBudgetJSON).
func (s *Stage) UnmarshalJSON(data []byte) error {
	if err := rejectBudgetJSON(data); err != nil {
		return err
	}
	type plain Stage // no methods: decoding it does not recurse into UnmarshalJSON
	return json.Unmarshal(data, (*plain)(s))
}

// UnmarshalJSON rejects a null or repeated Budget (see rejectBudgetJSON).
func (m *TeamMember) UnmarshalJSON(data []byte) error {
	if err := rejectBudgetJSON(data); err != nil {
		return err
	}
	type plain TeamMember // no methods: decoding it does not recurse into UnmarshalJSON
	return json.Unmarshal(data, (*plain)(m))
}

// UnmarshalJSON rejects a null or repeated Budget (see rejectBudgetJSON).
func (a *StageAssistant) UnmarshalJSON(data []byte) error {
	if err := rejectBudgetJSON(data); err != nil {
		return err
	}
	type plain StageAssistant // no methods: decoding it does not recurse into UnmarshalJSON
	return json.Unmarshal(data, (*plain)(a))
}

// rejectBudgetJSON guards flow values arriving as JSON (journal events, dynamic stages): a *budget.Spec
// field would read a null Budget as no budget. Every other field decodes as before.
func rejectBudgetJSON(data []byte) error { return budget.RejectNullField(data, "Budget") }
