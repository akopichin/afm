package mcpserver

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func listedTools(t *testing.T, h *harness) map[string]*sdkmcp.Tool {
	t.Helper()
	tools := map[string]*sdkmcp.Tool{}
	for tool, err := range h.connect(t, nil).Tools(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		tools[tool.Name] = tool
	}
	return tools
}

// mustJSON renders v with sorted keys, so a schema compares equal whether it is a struct or a decoded map.
func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatal(err)
	}
	sorted, err := json.Marshal(generic)
	if err != nil {
		t.Fatal(err)
	}
	return string(sorted)
}

// The service registers from the catalog, so a client sees exactly what Catalog() declares.
func TestToolsListMatchesCatalog(t *testing.T) {
	h := newHarness(t, "reply,stages,approve,budget")
	listed := listedTools(t, h)
	catalog := Catalog()
	if len(listed) != len(catalog) {
		t.Fatalf("service lists %d tools, catalog has %d", len(listed), len(catalog))
	}
	for _, spec := range catalog {
		tool, ok := listed[spec.Name]
		if !ok {
			t.Errorf("tool %s is in the catalog but not listed", spec.Name)
			continue
		}
		if tool.Description != spec.Description {
			t.Errorf("%s description differs from the catalog", spec.Name)
		}
		if mustJSON(t, tool.InputSchema) != mustJSON(t, spec.InputSchema) {
			t.Errorf("%s input schema differs from the catalog:\n%s\n%s", spec.Name, mustJSON(t, tool.InputSchema), mustJSON(t, spec.InputSchema))
		}
		if mustJSON(t, tool.Annotations) != mustJSON(t, spec.Annotations) {
			t.Errorf("%s annotations differ from the catalog", spec.Name)
		}
	}
}

func TestToolsListFollowsCatalogGroups(t *testing.T) {
	for _, groups := range []string{"", "reply", "stages", "approve", "budget", "reply,approve", "stages,budget"} {
		t.Run("groups="+groups, func(t *testing.T) {
			g, err := ParseGroups(groups)
			if err != nil {
				t.Fatal(err)
			}
			var want []string
			for _, spec := range Catalog() {
				if spec.Offered(g) {
					want = append(want, spec.Name)
				}
			}
			got := toolNames(t, newHarness(t, groups).connect(t, nil))
			slices.Sort(want)
			slices.Sort(got)
			if !slices.Equal(got, want) {
				t.Errorf("listed %v, want %v", got, want)
			}
		})
	}
}

func TestCatalogGroupsAndRunBinding(t *testing.T) {
	type facts struct {
		group    Group
		altGroup Group
		runBound bool
	}
	want := map[string]facts{
		"afm_run_status":        {},
		"afm_events_read":       {},
		"afm_artifact_read":     {},
		"afm_run_wait":          {runBound: true},
		"afm_interaction_reply": {group: GroupReply, runBound: true},
		"afm_stage_decide":      {group: GroupApprove, altGroup: GroupStages, runBound: true},
		"afm_stage_pause":       {group: GroupStages, runBound: true},
		"afm_stage_continue":    {group: GroupStages, runBound: true},
		"afm_budget_adjust":     {group: GroupBudget, runBound: true},
		"afm_budget_resume":     {group: GroupBudget, runBound: true},
	}
	catalog := Catalog()
	if len(catalog) != len(want) {
		t.Fatalf("catalog has %d tools, want %d", len(catalog), len(want))
	}
	for _, spec := range catalog {
		w, ok := want[spec.Name]
		if !ok {
			t.Errorf("unexpected tool %s", spec.Name)
			continue
		}
		if spec.Group != w.group || spec.AltGroup != w.altGroup || spec.RunBound != w.runBound {
			t.Errorf("%s = group %q alt %q run-bound %v, want %+v", spec.Name, spec.Group, spec.AltGroup, spec.RunBound, w)
		}
		// RunBound must say whether the schema really asks for run_id: the host strips it otherwise.
		schema := mustJSON(t, spec.InputSchema)
		var parsed struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal([]byte(schema), &parsed); err != nil {
			t.Fatal(err)
		}
		if _, has := parsed.Properties["run_id"]; has != spec.RunBound {
			t.Errorf("%s: schema has run_id = %v, RunBound = %v", spec.Name, has, spec.RunBound)
		}
	}
}

func TestResourceTemplatesMatchService(t *testing.T) {
	templates := ResourceTemplates()
	var uris []string
	for _, tpl := range templates {
		uris = append(uris, tpl.URITemplate)
	}
	want := []string{
		"afm://runs/{run_id}/status",
		"afm://runs/{run_id}/cost",
		"afm://runs/{run_id}/stages/{stage_id}/plans/{revision}",
		"afm://runs/{run_id}/artifacts/{artifact_id}",
	}
	if !slices.Equal(uris, want) {
		t.Fatalf("templates = %v, want %v", uris, want)
	}

	h := newHarness(t, "")
	session := h.connect(t, nil)
	listed := map[string]bool{}
	for res, err := range session.Resources(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		listed[res.URI] = true
	}
	if !listed[statusURI("run-1")] || !listed[costURI("run-1")] {
		t.Errorf("resources = %v, want status and cost of run-1", listed)
	}
	var listedTemplates []string
	for tpl, err := range session.ResourceTemplates(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		listedTemplates = append(listedTemplates, tpl.URITemplate)
	}
	slices.Sort(listedTemplates)
	if wantTemplates := slices.Sorted(slices.Values(want[2:])); !slices.Equal(listedTemplates, wantTemplates) {
		t.Errorf("service templates = %v, want %v", listedTemplates, wantTemplates)
	}
}
