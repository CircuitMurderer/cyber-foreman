package acpagent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

type Profile struct {
	Name         string   `json:"name"`
	Command      string   `json:"command"`
	Args         []string `json:"args,omitempty"`
	VersionArgs  []string `json:"version_args,omitempty"`
	AuthMethods  []string `json:"auth_methods,omitempty"`
	AuthOptional bool     `json:"auth_optional,omitempty"`
}

type ProfileFile struct {
	Agents []Profile `json:"agents"`
}

func OpenCodeProfile(command string) Profile {
	return Profile{
		Name: "opencode", Command: command, Args: []string{"acp"}, VersionArgs: []string{"--version"},
	}
}

func GrokProfile(command string) Profile {
	return Profile{
		Name: "grok", Command: command,
		Args: []string{"--no-auto-update", "agent", "stdio"}, VersionArgs: []string{"version"},
		AuthMethods: []string{"xai.api_key", "cached_token"}, AuthOptional: true,
	}
}

func LoadProfiles(path string) ([]Profile, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open ACP agent profiles: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var config ProfileFile
	if err := decoder.Decode(&config); err != nil {
		return nil, fmt.Errorf("decode ACP agent profiles: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("ACP agent profile file must contain one JSON object")
		}
		return nil, fmt.Errorf("decode trailing ACP agent profile data: %w", err)
	}
	if len(config.Agents) == 0 {
		return nil, errors.New("ACP agent profile file has no agents")
	}
	seen := make(map[string]struct{}, len(config.Agents))
	for i := range config.Agents {
		config.Agents[i].Name = strings.TrimSpace(config.Agents[i].Name)
		config.Agents[i].Command = strings.TrimSpace(config.Agents[i].Command)
		if err := config.Agents[i].Validate(); err != nil {
			return nil, fmt.Errorf("agents[%d]: %w", i, err)
		}
		if _, exists := seen[config.Agents[i].Name]; exists {
			return nil, fmt.Errorf("duplicate ACP agent name %q", config.Agents[i].Name)
		}
		seen[config.Agents[i].Name] = struct{}{}
	}
	return config.Agents, nil
}

func (p Profile) Validate() error {
	if strings.TrimSpace(p.Name) == "" {
		return errors.New("name is required")
	}
	if strings.TrimSpace(p.Command) == "" {
		return errors.New("command is required")
	}
	for _, value := range append(append(append([]string(nil), p.Args...), p.VersionArgs...), p.AuthMethods...) {
		if strings.ContainsRune(value, '\x00') {
			return errors.New("profile values cannot contain NUL")
		}
	}
	return nil
}
