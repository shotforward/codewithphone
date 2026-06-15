package templates

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const ManifestFileName = "template.yaml"

type InteractionModel string

const (
	InteractionModelPassthrough InteractionModel = "passthrough"
	InteractionModelSingleAgent InteractionModel = "single_agent"
	InteractionModelAgentGroup  InteractionModel = "agent_group"
)

type Template struct {
	Root                 string           `yaml:"-"`
	SchemaVersion        string           `yaml:"schema_version"`
	ID                   string           `yaml:"id"`
	Version              string           `yaml:"version"`
	DisplayName          string           `yaml:"display_name"`
	Description          string           `yaml:"description,omitempty"`
	InteractionModel     InteractionModel `yaml:"interaction_model"`
	Metadata             Metadata         `yaml:"metadata,omitempty"`
	Orchestrator         *Orchestrator    `yaml:"orchestrator,omitempty"`
	Agents               []Agent          `yaml:"agents,omitempty"`
	Artifacts            ArtifactConfig   `yaml:"artifacts,omitempty"`
	RequiredCapabilities []CapabilityRef  `yaml:"required_capabilities,omitempty"`
	Tools                []ToolRef        `yaml:"tools,omitempty"`
	Extensions           map[string]any   `yaml:"x,omitempty"`
}

type Metadata struct {
	Author     string   `yaml:"author,omitempty"`
	License    string   `yaml:"license,omitempty"`
	Repository string   `yaml:"repository,omitempty"`
	Tags       []string `yaml:"tags,omitempty"`
}

type Orchestrator struct {
	Playbook     string `yaml:"playbook,omitempty"`
	PlaybookPath string `yaml:"-"`
}

type Agent struct {
	ID           string   `yaml:"id"`
	DisplayName  string   `yaml:"display_name"`
	Mention      string   `yaml:"mention"`
	Description  string   `yaml:"description,omitempty"`
	Focus        string   `yaml:"focus,omitempty"`
	Prompt       string   `yaml:"prompt"`
	PromptPath   string   `yaml:"-"`
	Skills       []string `yaml:"skills,omitempty"`
	SkillPaths   []string `yaml:"-"`
	Tools        []string `yaml:"tools,omitempty"`
	ArtifactRoot string   `yaml:"artifact_root,omitempty"`
	Icon         string   `yaml:"icon,omitempty"`
	Color        string   `yaml:"color,omitempty"`
}

type ArtifactConfig struct {
	Root             string `yaml:"root,omitempty"`
	MarkdownFirst    bool   `yaml:"markdown_first,omitempty"`
	MaxMarkdownBytes int    `yaml:"max_markdown_bytes,omitempty"`
}

type CapabilityRef struct {
	ID               string `yaml:"id"`
	Required         bool   `yaml:"required,omitempty"`
	ApprovalRequired bool   `yaml:"approval_required,omitempty"`
	Reason           string `yaml:"reason,omitempty"`
}

type ToolRef struct {
	ID            string `yaml:"id"`
	Manifest      string `yaml:"manifest,omitempty"`
	ManifestPath  string `yaml:"-"`
	Executable    bool   `yaml:"executable,omitempty"`
	TrustRequired bool   `yaml:"trust_required,omitempty"`
}

func Load(root string) (*Template, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, fmt.Errorf("template root is required")
	}

	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve template root: %w", err)
	}

	payload, err := os.ReadFile(filepath.Join(absRoot, ManifestFileName))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", ManifestFileName, err)
	}

	var tmpl Template
	decoder := yaml.NewDecoder(bytes.NewReader(payload))
	decoder.KnownFields(true)
	if err := decoder.Decode(&tmpl); err != nil {
		return nil, fmt.Errorf("parse %s: %w", ManifestFileName, err)
	}
	tmpl.Root = absRoot

	if err := tmpl.validateAndResolve(); err != nil {
		return nil, err
	}
	return &tmpl, nil
}

func (t *Template) validateAndResolve() error {
	if strings.TrimSpace(t.SchemaVersion) == "" {
		return fmt.Errorf("template schema_version is required")
	}
	if strings.TrimSpace(t.ID) == "" {
		return fmt.Errorf("template id is required")
	}
	if strings.TrimSpace(t.Version) == "" {
		return fmt.Errorf("template version is required")
	}
	if strings.TrimSpace(t.DisplayName) == "" {
		return fmt.Errorf("template display_name is required")
	}

	switch t.InteractionModel {
	case InteractionModelPassthrough:
		if len(t.Agents) != 0 {
			return fmt.Errorf("passthrough template must not define agents")
		}
	case InteractionModelSingleAgent:
		if len(t.Agents) != 1 {
			return fmt.Errorf("single_agent template must define exactly one agent")
		}
	case InteractionModelAgentGroup:
		if len(t.Agents) == 0 {
			return fmt.Errorf("agent_group template must define at least one agent")
		}
	default:
		return fmt.Errorf("invalid interaction_model %q", t.InteractionModel)
	}

	if t.Orchestrator != nil && strings.TrimSpace(t.Orchestrator.Playbook) != "" {
		path, err := resolveExistingFile(t.Root, t.Orchestrator.Playbook, "orchestrator.playbook")
		if err != nil {
			return err
		}
		t.Orchestrator.PlaybookPath = path
	}

	ids := map[string]struct{}{}
	mentions := map[string]struct{}{}
	for i := range t.Agents {
		if err := t.Agents[i].validateAndResolve(t.Root, ids, mentions); err != nil {
			return err
		}
	}

	toolIDs := map[string]struct{}{}
	for i := range t.Tools {
		if err := t.Tools[i].validateAndResolve(t.Root, toolIDs); err != nil {
			return err
		}
	}

	return nil
}

func (a *Agent) validateAndResolve(root string, ids map[string]struct{}, mentions map[string]struct{}) error {
	if strings.TrimSpace(a.ID) == "" {
		return fmt.Errorf("agent id is required")
	}
	if _, exists := ids[a.ID]; exists {
		return fmt.Errorf("duplicate agent id %q", a.ID)
	}
	ids[a.ID] = struct{}{}

	if strings.TrimSpace(a.DisplayName) == "" {
		return fmt.Errorf("agent %q display_name is required", a.ID)
	}
	if strings.TrimSpace(a.Mention) == "" {
		return fmt.Errorf("agent %q mention is required", a.ID)
	}
	if !strings.HasPrefix(a.Mention, "@") {
		return fmt.Errorf("agent %q mention must start with @", a.ID)
	}
	if _, exists := mentions[a.Mention]; exists {
		return fmt.Errorf("duplicate agent mention %q", a.Mention)
	}
	mentions[a.Mention] = struct{}{}

	promptPath, err := resolveExistingFile(root, a.Prompt, fmt.Sprintf("agent %q prompt", a.ID))
	if err != nil {
		return err
	}
	a.PromptPath = promptPath

	a.SkillPaths = make([]string, 0, len(a.Skills))
	for _, skill := range a.Skills {
		path, err := resolveExistingFile(root, skill, fmt.Sprintf("agent %q skill", a.ID))
		if err != nil {
			return err
		}
		a.SkillPaths = append(a.SkillPaths, path)
	}

	if strings.TrimSpace(a.ArtifactRoot) != "" {
		if _, err := resolveUnderRoot(root, a.ArtifactRoot, fmt.Sprintf("agent %q artifact_root", a.ID)); err != nil {
			return err
		}
	}

	return nil
}

func (t *ToolRef) validateAndResolve(root string, ids map[string]struct{}) error {
	if strings.TrimSpace(t.ID) == "" {
		return fmt.Errorf("tool id is required")
	}
	if _, exists := ids[t.ID]; exists {
		return fmt.Errorf("duplicate tool id %q", t.ID)
	}
	ids[t.ID] = struct{}{}

	if strings.TrimSpace(t.Manifest) == "" {
		return nil
	}

	path, err := resolveExistingFile(root, t.Manifest, fmt.Sprintf("tool %q manifest", t.ID))
	if err != nil {
		return err
	}
	t.ManifestPath = path
	return nil
}

func resolveExistingFile(root string, rawPath string, label string) (string, error) {
	path, err := resolveUnderRoot(root, rawPath, label)
	if err != nil {
		return "", err
	}

	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("%s %q is not readable: %w", label, rawPath, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s %q must be a file", label, rawPath)
	}
	return path, nil
}

func resolveUnderRoot(root string, rawPath string, label string) (string, error) {
	rawPath = strings.TrimSpace(rawPath)
	if rawPath == "" {
		return "", fmt.Errorf("%s path is required", label)
	}
	if filepath.IsAbs(rawPath) {
		return "", fmt.Errorf("%s %q must be relative to template root", label, rawPath)
	}

	candidate := filepath.Clean(filepath.Join(root, rawPath))
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return "", fmt.Errorf("resolve %s %q: %w", label, rawPath, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s %q escapes template root", label, rawPath)
	}
	return candidate, nil
}
