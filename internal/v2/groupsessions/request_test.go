package groupsessions

import (
	"path/filepath"
	"testing"

	"github.com/shotforward/codewithphone/internal/v2/templates"
)

func TestBuildCreateRequestFromSDLCTemplate(t *testing.T) {
	tmpl, err := templates.Load(filepath.Join("..", "templates", "testdata", "sdlc"))
	if err != nil {
		t.Fatalf("load template: %v", err)
	}

	req, err := BuildCreateRequest(tmpl, CreateInput{
		Title:         "Build v2 group session",
		MachineID:     "machine_001",
		Runtime:       "codex_cli",
		WorkspaceRoot: "/workspace/project",
	})
	if err != nil {
		t.Fatalf("BuildCreateRequest() error = %v", err)
	}

	if req.TemplateID != "codewithphone/sdlc" {
		t.Fatalf("TemplateID = %q", req.TemplateID)
	}
	if req.InteractionModel != "" {
		t.Fatalf("InteractionModel = %q, want empty for server-resolved template", req.InteractionModel)
	}
	if len(req.Agents) != 0 {
		t.Fatalf("len(Agents) = %d, want 0 for server-resolved template", len(req.Agents))
	}
}

func TestBuildCreateRequestKeepsCustomTemplateAgents(t *testing.T) {
	req, err := BuildCreateRequest(&templates.Template{
		ID:               "example/custom-sdlc",
		InteractionModel: templates.InteractionModelAgentGroup,
		Agents: []templates.Agent{{
			ID:          "pm",
			DisplayName: "Product Manager",
			Mention:     "@pm",
		}},
	}, CreateInput{
		Title:         "Build custom group session",
		MachineID:     "machine_001",
		Runtime:       "codex_cli",
		WorkspaceRoot: "/workspace/project",
	})
	if err != nil {
		t.Fatalf("BuildCreateRequest() error = %v", err)
	}
	if req.InteractionModel != "agent_group" {
		t.Fatalf("InteractionModel = %q", req.InteractionModel)
	}
	if len(req.Agents) != 1 || req.Agents[0].AgentID != "pm" {
		t.Fatalf("Agents = %+v", req.Agents)
	}
}

func TestBuildCreateRequestRejectsMissingTitle(t *testing.T) {
	tmpl, err := templates.Load(filepath.Join("..", "templates", "testdata", "default"))
	if err != nil {
		t.Fatalf("load template: %v", err)
	}

	_, err = BuildCreateRequest(tmpl, CreateInput{})
	if err == nil {
		t.Fatal("BuildCreateRequest() error = nil, want error")
	}
}
