package store

import (
	"encoding/json"
	"os"
	"sync"
)

type ModelMapping struct {
	CopilotID   string `json:"copilotId"`
	DisplayID   string `json:"displayId"`
	DisplayName string `json:"displayName,omitempty"`
}

type ModelMapStore struct {
	Mappings []ModelMapping `json:"mappings"`
}

var modelMapMu sync.RWMutex

func readModelMap() (*ModelMapStore, error) {
	data, err := os.ReadFile(ModelMapFile())
	if err != nil {
		return &ModelMapStore{}, nil
	}
	if len(data) == 0 || string(data) == "{}" {
		return &ModelMapStore{}, nil
	}
	var s ModelMapStore
	if err := json.Unmarshal(data, &s); err != nil {
		return &ModelMapStore{}, nil
	}
	return &s, nil
}

func writeModelMap(s *ModelMapStore) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(ModelMapFile(), data, 0644)
}

func GetModelMappings() ([]ModelMapping, error) {
	modelMapMu.RLock()
	defer modelMapMu.RUnlock()
	s, err := readModelMap()
	if err != nil {
		return nil, err
	}
	if s.Mappings == nil {
		return []ModelMapping{}, nil
	}
	return s.Mappings, nil
}

func SetModelMappings(mappings []ModelMapping) error {
	modelMapMu.Lock()
	defer modelMapMu.Unlock()
	s := &ModelMapStore{Mappings: mappings}
	return writeModelMap(s)
}

func AddModelMapping(mapping ModelMapping) error {
	modelMapMu.Lock()
	defer modelMapMu.Unlock()
	s, err := readModelMap()
	if err != nil {
		return err
	}
	// Replace if copilotId already exists
	found := false
	for i, m := range s.Mappings {
		if m.CopilotID == mapping.CopilotID {
			s.Mappings[i] = mapping
			found = true
			break
		}
	}
	if !found {
		s.Mappings = append(s.Mappings, mapping)
	}
	return writeModelMap(s)
}

func DeleteModelMapping(copilotID string) error {
	modelMapMu.Lock()
	defer modelMapMu.Unlock()
	s, err := readModelMap()
	if err != nil {
		return err
	}
	var filtered []ModelMapping
	for _, m := range s.Mappings {
		if m.CopilotID != copilotID {
			filtered = append(filtered, m)
		}
	}
	s.Mappings = filtered
	return writeModelMap(s)
}

func ToDisplayID(copilotID string) string {
	modelMapMu.RLock()
	defer modelMapMu.RUnlock()
	s, err := readModelMap()
	if err != nil {
		return copilotID
	}
	for _, m := range s.Mappings {
		if m.CopilotID == copilotID {
			return m.DisplayID
		}
	}
	return copilotID
}

func ToCopilotID(displayID string) string {
	modelMapMu.RLock()
	defer modelMapMu.RUnlock()
	s, err := readModelMap()
	if err != nil {
		return displayID
	}
	for _, m := range s.Mappings {
		if m.DisplayID == displayID {
			return m.CopilotID
		}
	}
	return displayID
}
