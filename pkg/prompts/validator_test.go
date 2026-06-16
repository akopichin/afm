package prompts

import (
	"reflect"
	"testing"
)

func TestValidatePlan(t *testing.T) {
	cases := []struct {
		name     string
		md       string
		required []string
		want     []string
	}{
		{
			name:     "all present",
			required: []string{"Tasks", "Assumptions", "Acceptance Criteria"},
			md:       "# X\n## Tasks\n- a\n## Assumptions\n- none\n## Acceptance Criteria\n- [ ] x",
			want:     nil,
		},
		{
			name:     "missing assumptions",
			required: []string{"Tasks", "Assumptions", "Acceptance Criteria"},
			md:       "# X\n## Tasks\n- a\n## Acceptance Criteria\n- [ ] x",
			want:     []string{"Assumptions"},
		},
		{
			name:     "missing two",
			required: []string{"Tasks", "Assumptions", "Acceptance Criteria"},
			md:       "# X\n## Tasks\n- a",
			want:     []string{"Assumptions", "Acceptance Criteria"},
		},
		{
			name:     "extra heading ok",
			required: []string{"Tasks"},
			md:       "## Overview\nfoo\n## Tasks\n- a\n## Notes\nbar",
			want:     nil,
		},
		{
			name:     "case-insensitive match",
			required: []string{"Acceptance Criteria"},
			md:       "## ACCEPTANCE CRITERIA\n- [ ] x",
			want:     nil,
		},
		{
			// Планирующий агент пишет разделы на языке проекта (для русскоязычных
			// флоу — «Задачи»/«Допущения»). Валидатор обязан принимать такие заголовки.
			name:     "russian headings accepted",
			required: []string{"Tasks", "Assumptions", "Acceptance Criteria"},
			md:       "## Задачи\n- a\n## Допущения\n- none\n## Acceptance Criteria\n- [ ] x",
			want:     nil,
		},
		{
			name:     "fully russian plan accepted",
			required: []string{"Tasks", "Assumptions", "Acceptance Criteria"},
			md:       "## Задачи\n- a\n## Допущения\n- none\n## Критерии приёмки\n- [ ] x",
			want:     nil,
		},
		{
			name:     "empty string",
			required: []string{"Tasks"},
			md:       "",
			want:     []string{"Tasks"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ValidatePlan(tc.md, tc.required).MissingSections
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("missing = %v, want %v", got, tc.want)
			}
		})
	}
}
