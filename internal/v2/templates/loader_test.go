package templates

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadPassthroughTemplate(t *testing.T) {
	root := writeTemplate(t, map[string]string{
		"template.yaml": `
schema_version: v2
id: codewithphone/plain
version: 0.1.0
display_name: Plain
interaction_model: passthrough
`,
	})

	tmpl, err := Load(root)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if tmpl.Root != root {
		t.Fatalf("Root = %q, want %q", tmpl.Root, root)
	}
	if tmpl.InteractionModel != InteractionModelPassthrough {
		t.Fatalf("InteractionModel = %q", tmpl.InteractionModel)
	}
}

func TestLoadDefaultFixture(t *testing.T) {
	tmpl, err := Load(filepath.Join("testdata", "default"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	// New v2 official Template IDs use codewithphone/*; legacy PocketCode
	// names stay in existing code until a separate migration.
	if tmpl.ID != "codewithphone/default" {
		t.Fatalf("ID = %q", tmpl.ID)
	}
	if tmpl.InteractionModel != InteractionModelPassthrough {
		t.Fatalf("InteractionModel = %q", tmpl.InteractionModel)
	}
}

func TestLoadAgentGroupTemplate(t *testing.T) {
	root := writeTemplate(t, map[string]string{
		"template.yaml": `
schema_version: v2
id: codewithphone/sdlc
version: 0.1.0
display_name: SDLC Team
interaction_model: agent_group
orchestrator:
  playbook: orchestrator/playbook.md
agents:
  - id: pm
    display_name: Product Manager
    mention: "@pm"
    prompt: agents/pm.md
    skills:
      - skills/prd.md
tools:
  - id: xhs.search
    manifest: tools/xhs-search/tool.yaml
    executable: true
    trust_required: true
`,
		"orchestrator/playbook.md":   "# Playbook\n",
		"agents/pm.md":               "# PM\n",
		"skills/prd.md":              "# PRD\n",
		"tools/xhs-search/tool.yaml": "name: xhs.search\n",
	})

	tmpl, err := Load(root)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if len(tmpl.Agents) != 1 {
		t.Fatalf("len(Agents) = %d, want 1", len(tmpl.Agents))
	}
	if tmpl.Agents[0].PromptPath != filepath.Join(root, "agents/pm.md") {
		t.Fatalf("PromptPath = %q", tmpl.Agents[0].PromptPath)
	}
	if got := tmpl.Agents[0].SkillPaths[0]; got != filepath.Join(root, "skills/prd.md") {
		t.Fatalf("SkillPaths[0] = %q", got)
	}
	if tmpl.Orchestrator.PlaybookPath != filepath.Join(root, "orchestrator/playbook.md") {
		t.Fatalf("PlaybookPath = %q", tmpl.Orchestrator.PlaybookPath)
	}
	if tmpl.Tools[0].ManifestPath != filepath.Join(root, "tools/xhs-search/tool.yaml") {
		t.Fatalf("ManifestPath = %q", tmpl.Tools[0].ManifestPath)
	}
}

func TestLoadSDLCDogfoodFixture(t *testing.T) {
	tmpl, err := Load(filepath.Join("testdata", "sdlc"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	// New v2 official Template IDs use codewithphone/*; legacy PocketCode
	// names stay in existing code until a separate migration.
	if tmpl.ID != "codewithphone/sdlc" {
		t.Fatalf("ID = %q", tmpl.ID)
	}
	if tmpl.InteractionModel != InteractionModelAgentGroup {
		t.Fatalf("InteractionModel = %q", tmpl.InteractionModel)
	}
	if len(tmpl.Agents) != 6 {
		t.Fatalf("len(Agents) = %d, want 6", len(tmpl.Agents))
	}
	lastAgent := tmpl.Agents[len(tmpl.Agents)-1]
	if lastAgent.ID != "ops" || lastAgent.Mention != "@ops" {
		t.Fatalf("last agent = %+v, want ops/@ops", lastAgent)
	}
	if tmpl.Orchestrator == nil || tmpl.Orchestrator.PlaybookPath == "" {
		t.Fatalf("orchestrator playbook was not resolved")
	}
}

func TestLoadRejectsMissingManifest(t *testing.T) {
	root := writeTemplate(t, map[string]string{})

	_, err := Load(root)
	if err == nil {
		t.Fatal("Load() error = nil, want error")
	}
	assertErrorContains(t, err, "read template.yaml")
}

func TestLoadRejectsUnknownManifestField(t *testing.T) {
	root := writeTemplate(t, map[string]string{
		"template.yaml": `
schema_version: v2
id: codewithphone/plain
version: 0.1.0
display_name: Plain
interaction_model: passthrough
unknown_field: true
`,
	})

	_, err := Load(root)
	if err == nil {
		t.Fatal("Load() error = nil, want error")
	}
	assertErrorContains(t, err, "field unknown_field not found")
}

func TestLoadRejectsInvalidInteractionModel(t *testing.T) {
	root := writeTemplate(t, map[string]string{
		"template.yaml": `
schema_version: v2
id: codewithphone/plain
version: 0.1.0
display_name: Plain
interaction_model: workflow
`,
	})

	_, err := Load(root)
	if err == nil {
		t.Fatal("Load() error = nil, want error")
	}
	assertErrorContains(t, err, `invalid interaction_model "workflow"`)
}

func TestLoadRejectsAgentGroupWithoutAgents(t *testing.T) {
	root := writeTemplate(t, map[string]string{
		"template.yaml": `
schema_version: v2
id: codewithphone/sdlc
version: 0.1.0
display_name: SDLC Team
interaction_model: agent_group
`,
	})

	_, err := Load(root)
	if err == nil {
		t.Fatal("Load() error = nil, want error")
	}
	assertErrorContains(t, err, "agent_group template must define at least one agent")
}

func TestLoadRejectsMissingAgentPrompt(t *testing.T) {
	root := writeTemplate(t, map[string]string{
		"template.yaml": `
schema_version: v2
id: codewithphone/sdlc
version: 0.1.0
display_name: SDLC Team
interaction_model: agent_group
agents:
  - id: pm
    display_name: Product Manager
    mention: "@pm"
    prompt: agents/missing.md
`,
	})

	_, err := Load(root)
	if err == nil {
		t.Fatal("Load() error = nil, want error")
	}
	assertErrorContains(t, err, `agent "pm" prompt "agents/missing.md" is not readable`)
}

func TestLoadRejectsEscapedPromptPath(t *testing.T) {
	root := writeTemplate(t, map[string]string{
		"template.yaml": `
schema_version: v2
id: codewithphone/sdlc
version: 0.1.0
display_name: SDLC Team
interaction_model: agent_group
agents:
  - id: pm
    display_name: Product Manager
    mention: "@pm"
    prompt: ../outside.md
`,
	})

	_, err := Load(root)
	if err == nil {
		t.Fatal("Load() error = nil, want error")
	}
	assertErrorContains(t, err, `agent "pm" prompt "../outside.md" escapes template root`)
}

func TestLoadRejectsDuplicateAgentMention(t *testing.T) {
	root := writeTemplate(t, map[string]string{
		"template.yaml": `
schema_version: v2
id: codewithphone/sdlc
version: 0.1.0
display_name: SDLC Team
interaction_model: agent_group
agents:
  - id: pm
    display_name: Product Manager
    mention: "@same"
    prompt: agents/pm.md
  - id: qa
    display_name: QA
    mention: "@same"
    prompt: agents/qa.md
`,
		"agents/pm.md": "# PM\n",
		"agents/qa.md": "# QA\n",
	})

	_, err := Load(root)
	if err == nil {
		t.Fatal("Load() error = nil, want error")
	}
	assertErrorContains(t, err, `duplicate agent mention "@same"`)
}

func writeTemplate(t *testing.T, files map[string]string) string {
	t.Helper()

	root, err := filepath.Abs(t.TempDir())
	if err != nil {
		t.Fatalf("Abs() error = %v", err)
	}
	for path, content := range files {
		fullPath := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(fullPath), err)
		}
		if err := os.WriteFile(fullPath, []byte(strings.TrimPrefix(content, "\n")), 0o644); err != nil {
			t.Fatalf("WriteFile(%q) error = %v", fullPath, err)
		}
	}
	return root
}

func assertErrorContains(t *testing.T, err error, want string) {
	t.Helper()
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %q, want substring %q", err.Error(), want)
	}
}
