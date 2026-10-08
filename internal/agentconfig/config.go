package agentconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"cyber-foreman/internal/agent"
	"cyber-foreman/internal/agent/acpagent"
	"cyber-foreman/internal/agent/codex"
)

const (
	DriverACP   = "acp"
	DriverCodex = "codex-app-server"

	FormatOpenAI    = "openai"
	FormatAnthropic = "anthropic"
	FormatGoogle    = "google"
)

type File struct {
	Agents   []Profile `json:"agents"`
	Security *Security `json:"security,omitempty"`
}

// Security contains deployment-wide boundaries. Nil slices mean unrestricted
// for backwards compatibility; explicit empty slices deny every value.
type Security struct {
	WorkspaceRoots   []string   `json:"workspace_roots,omitempty"`
	CommandAllowlist [][]string `json:"command_allowlist,omitempty"`
	APITokenEnv      string     `json:"api_token_env,omitempty"`
}

type Profile struct {
	Name             string    `json:"name"`
	Driver           string    `json:"driver"`
	Command          string    `json:"command"`
	Args             []string  `json:"args,omitempty"`
	VersionArgs      []string  `json:"version_args,omitempty"`
	AuthMethods      []string  `json:"auth_methods,omitempty"`
	AuthOptional     bool      `json:"auth_optional,omitempty"`
	DefaultModel     string    `json:"default_model,omitempty"`
	DefaultWorkspace string    `json:"default_workspace,omitempty"`
	Provider         *Provider `json:"provider,omitempty"`
}

type Provider struct {
	Format    string `json:"format"`
	WireAPI   string `json:"wire_api,omitempty"`
	BaseURL   string `json:"base_url,omitempty"`
	APIKeyEnv string `json:"api_key_env,omitempty"`
}

func Load(path string) ([]Profile, error) {
	config, err := LoadFile(path)
	if err != nil {
		return nil, err
	}
	return config.Agents, nil
}

func LoadFile(path string) (File, error) {
	file, err := os.Open(path)
	if err != nil {
		return File{}, fmt.Errorf("open agent configuration: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var config File
	if err := decoder.Decode(&config); err != nil {
		return File{}, fmt.Errorf("decode agent configuration: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return File{}, errors.New("agent configuration must contain one JSON object")
		}
		return File{}, fmt.Errorf("decode trailing agent configuration: %w", err)
	}
	if len(config.Agents) == 0 {
		return File{}, errors.New("agent configuration has no agents")
	}
	seen := make(map[string]struct{}, len(config.Agents))
	for index := range config.Agents {
		config.Agents[index].normalize()
		if err := config.Agents[index].Validate(); err != nil {
			return File{}, fmt.Errorf("agents[%d]: %w", index, err)
		}
		if _, exists := seen[config.Agents[index].Name]; exists {
			return File{}, fmt.Errorf("duplicate agent name %q", config.Agents[index].Name)
		}
		seen[config.Agents[index].Name] = struct{}{}
	}
	if config.Security != nil {
		config.Security.normalize()
		if err := config.Security.Validate(); err != nil {
			return File{}, fmt.Errorf("security: %w", err)
		}
	}
	return config, nil
}

func (s *Security) normalize() {
	for index := range s.WorkspaceRoots {
		s.WorkspaceRoots[index] = filepath.Clean(strings.TrimSpace(s.WorkspaceRoots[index]))
	}
	for i := range s.CommandAllowlist {
		for j := range s.CommandAllowlist[i] {
			s.CommandAllowlist[i][j] = strings.TrimSpace(s.CommandAllowlist[i][j])
		}
	}
	s.APITokenEnv = strings.TrimSpace(s.APITokenEnv)
}

func (s Security) Validate() error {
	for index, root := range s.WorkspaceRoots {
		if root == "" || !filepath.IsAbs(root) {
			return fmt.Errorf("workspace_roots[%d] must be an absolute path", index)
		}
		if strings.ContainsRune(root, '\x00') {
			return fmt.Errorf("workspace_roots[%d] cannot contain NUL", index)
		}
	}
	for index, prefix := range s.CommandAllowlist {
		if len(prefix) == 0 {
			return fmt.Errorf("command_allowlist[%d] must contain at least one argument", index)
		}
		for _, value := range prefix {
			if value == "" || strings.ContainsRune(value, '\x00') {
				return fmt.Errorf("command_allowlist[%d] contains an empty or invalid argument", index)
			}
		}
	}
	if s.APITokenEnv != "" && !validEnvironmentName(s.APITokenEnv) {
		return fmt.Errorf("invalid api_token_env %q", s.APITokenEnv)
	}
	return nil
}

func (p *Profile) normalize() {
	p.Name = strings.TrimSpace(p.Name)
	p.Driver = strings.TrimSpace(p.Driver)
	p.Command = strings.TrimSpace(p.Command)
	p.DefaultModel = strings.TrimSpace(p.DefaultModel)
	p.DefaultWorkspace = strings.TrimSpace(p.DefaultWorkspace)
	if p.Provider != nil {
		p.Provider.Format = strings.TrimSpace(p.Provider.Format)
		p.Provider.WireAPI = strings.TrimSpace(p.Provider.WireAPI)
		p.Provider.BaseURL = strings.TrimRight(strings.TrimSpace(p.Provider.BaseURL), "/")
		p.Provider.APIKeyEnv = strings.TrimSpace(p.Provider.APIKeyEnv)
	}
}

func (p Profile) Validate() error {
	if p.Name == "" {
		return errors.New("name is required")
	}
	if p.Command == "" {
		return errors.New("command is required")
	}
	switch p.Driver {
	case DriverACP, DriverCodex:
	default:
		return fmt.Errorf("unsupported driver %q", p.Driver)
	}
	for _, value := range append(append(append([]string(nil), p.Args...), p.VersionArgs...), p.AuthMethods...) {
		if strings.ContainsRune(value, '\x00') {
			return errors.New("agent configuration values cannot contain NUL")
		}
	}
	if strings.ContainsRune(p.DefaultWorkspace, '\x00') || strings.ContainsRune(p.DefaultModel, '\x00') {
		return errors.New("agent defaults cannot contain NUL")
	}
	if p.DefaultWorkspace != "" && !filepath.IsAbs(p.DefaultWorkspace) {
		return errors.New("default_workspace must be an absolute path")
	}
	if p.Provider == nil {
		return nil
	}
	switch p.Provider.Format {
	case FormatOpenAI, FormatAnthropic, FormatGoogle:
	default:
		return fmt.Errorf("unsupported provider format %q", p.Provider.Format)
	}
	if p.DefaultModel == "" {
		return errors.New("default_model is required when provider is configured")
	}
	if p.Provider.BaseURL != "" {
		parsed, err := url.Parse(p.Provider.BaseURL)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return fmt.Errorf("invalid provider base_url %q", p.Provider.BaseURL)
		}
	}
	if p.Driver == DriverCodex && p.Provider.BaseURL == "" {
		return errors.New("provider base_url is required for codex-app-server")
	}
	if p.Provider.APIKeyEnv != "" && !validEnvironmentName(p.Provider.APIKeyEnv) {
		return fmt.Errorf("invalid provider api_key_env %q", p.Provider.APIKeyEnv)
	}
	if p.Provider.WireAPI != "" && p.Provider.WireAPI != "chat-completions" && p.Provider.WireAPI != "responses" {
		return fmt.Errorf("unsupported provider wire_api %q", p.Provider.WireAPI)
	}
	if p.Driver == DriverACP && p.Provider.WireAPI != "" {
		return errors.New("provider wire_api is only supported by codex-app-server")
	}
	if p.Provider.Format != FormatOpenAI && p.Provider.WireAPI == "responses" {
		return fmt.Errorf("provider format %q does not support responses wire_api", p.Provider.Format)
	}
	return nil
}

func validEnvironmentName(value string) bool {
	for index, character := range value {
		if character == '_' || character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z' || index > 0 && character >= '0' && character <= '9' {
			continue
		}
		return false
	}
	return value != ""
}

func Build(profile Profile) (agent.Adapter, error) {
	profile.normalize()
	if err := profile.Validate(); err != nil {
		return nil, err
	}
	var adapter agent.Adapter
	var err error
	switch profile.Driver {
	case DriverACP:
		adapter, err = acpagent.NewAdapter(acpagent.Config{
			Profile: acpagent.Profile{
				Name: profile.Name, Command: profile.Command, Args: profile.Args,
				VersionArgs: profile.VersionArgs, AuthMethods: profile.AuthMethods,
				AuthOptional: profile.AuthOptional,
			},
			Env: providerEnvironment(profile),
		})
	case DriverCodex:
		config := codex.Config{Name: profile.Name, Binary: profile.Command, Args: profile.Args}
		if profile.Provider != nil {
			config.Provider = &codex.ProviderConfig{
				BaseURL: profile.Provider.BaseURL, APIKeyEnv: profile.Provider.APIKeyEnv,
				DefaultModel: profile.DefaultModel, Protocol: codexProtocol(*profile.Provider),
			}
		}
		adapter, err = codex.NewAdapter(config)
	}
	if err != nil {
		return nil, err
	}
	format := ""
	if profile.Provider != nil {
		format = profile.Provider.Format
	}
	return agent.WithMetadata(adapter, agent.Metadata{
		Selectable: true, Driver: profile.Driver, ProviderFormat: format,
		DefaultModel: profile.DefaultModel, DefaultWorkspace: profile.DefaultWorkspace,
	}), nil
}

func providerEnvironment(profile Profile) []string {
	if profile.Provider == nil {
		return nil
	}
	values := []string{
		"FOREMAN_PROVIDER_FORMAT=" + profile.Provider.Format,
		"FOREMAN_PROVIDER_BASE_URL=" + profile.Provider.BaseURL,
		"FOREMAN_PROVIDER_API_KEY_ENV=" + profile.Provider.APIKeyEnv,
		"FOREMAN_PROVIDER_DEFAULT_MODEL=" + profile.DefaultModel,
	}
	return values
}

func codexProtocol(provider Provider) codex.ProviderProtocol {
	if provider.Format == FormatAnthropic {
		return codex.ProtocolAnthropic
	}
	if provider.WireAPI == "responses" {
		return codex.ProtocolResponses
	}
	return codex.ProtocolChatCompletions
}
