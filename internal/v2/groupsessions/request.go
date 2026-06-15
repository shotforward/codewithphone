package groupsessions

import (
	"fmt"
	"strings"

	"github.com/shotforward/codewithphone/internal/v2/templates"
)

type CreateInput struct {
	Title         string
	TitleIntent   string
	ProjectID     string
	RuntimeTarget string
	MachineID     string
	Runtime       string
	Model         string
	WorkspaceRoot string
}

type CreateRequest struct {
	Title            string         `json:"title"`
	TitleIntent      string         `json:"titleIntent,omitempty"`
	ProjectID        string         `json:"projectId,omitempty"`
	RuntimeTarget    string         `json:"runtimeTargetId,omitempty"`
	MachineID        string         `json:"machineId,omitempty"`
	Runtime          string         `json:"runtime,omitempty"`
	Model            string         `json:"model,omitempty"`
	WorkspaceRoot    string         `json:"workspaceRoot,omitempty"`
	TemplateID       string         `json:"templateId"`
	InteractionModel string         `json:"interactionModel,omitempty"`
	Agents           []AgentRequest `json:"agents,omitempty"`
}

type AgentRequest struct {
	AgentID      string `json:"agentId"`
	DisplayName  string `json:"displayName"`
	Mention      string `json:"mention"`
	ArtifactRoot string `json:"artifactRoot,omitempty"`
}

func BuildCreateRequest(tmpl *templates.Template, input CreateInput) (CreateRequest, error) {
	if tmpl == nil {
		return CreateRequest{}, fmt.Errorf("template is required")
	}
	title := strings.TrimSpace(input.Title)
	if title == "" {
		return CreateRequest{}, fmt.Errorf("title is required")
	}

	req := CreateRequest{
		Title:         title,
		TitleIntent:   strings.TrimSpace(input.TitleIntent),
		ProjectID:     strings.TrimSpace(input.ProjectID),
		RuntimeTarget: strings.TrimSpace(input.RuntimeTarget),
		MachineID:     strings.TrimSpace(input.MachineID),
		Runtime:       strings.TrimSpace(input.Runtime),
		Model:         strings.TrimSpace(input.Model),
		WorkspaceRoot: strings.TrimSpace(input.WorkspaceRoot),
		TemplateID:    strings.TrimSpace(tmpl.ID),
	}
	if req.TemplateID == "" {
		return CreateRequest{}, fmt.Errorf("template id is required")
	}

	// New v2 official Template IDs use codewithphone/*; legacy PocketCode
	// names stay in existing code until a separate migration. Official
	// templates are resolved server-side so the daemon only sends templateId.
	if isServerResolvedTemplate(req.TemplateID) {
		return req, nil
	}

	req.InteractionModel = string(tmpl.InteractionModel)
	if req.InteractionModel == "" {
		return CreateRequest{}, fmt.Errorf("template interaction model is required")
	}

	req.Agents = make([]AgentRequest, 0, len(tmpl.Agents))
	for _, agent := range tmpl.Agents {
		agentReq := AgentRequest{
			AgentID:      strings.TrimSpace(agent.ID),
			DisplayName:  strings.TrimSpace(agent.DisplayName),
			Mention:      strings.TrimSpace(agent.Mention),
			ArtifactRoot: strings.TrimSpace(agent.ArtifactRoot),
		}
		if agentReq.AgentID == "" {
			return CreateRequest{}, fmt.Errorf("template agent id is required")
		}
		if agentReq.DisplayName == "" {
			return CreateRequest{}, fmt.Errorf("template agent %q display name is required", agentReq.AgentID)
		}
		if agentReq.Mention == "" {
			return CreateRequest{}, fmt.Errorf("template agent %q mention is required", agentReq.AgentID)
		}
		req.Agents = append(req.Agents, agentReq)
	}

	return req, nil
}

func isServerResolvedTemplate(templateID string) bool {
	switch strings.TrimSpace(templateID) {
	case "codewithphone/default", "codewithphone/sdlc":
		return true
	default:
		return false
	}
}
