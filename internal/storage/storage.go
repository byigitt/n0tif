package storage

import (
	"encoding/json"
	"os"
	"path/filepath"
)

const (
	appFolderName = "n0tif"
	stateFileName = "email_state.json"
)

// EmailState stores information about previously seen emails
type EmailState struct {
	LastUIDs map[string][]uint32 `json:"last_uids"` // Maps mailbox to last seen UIDs
}

// NewEmailState creates a new email state
func NewEmailState() *EmailState {
	return &EmailState{
		LastUIDs: make(map[string][]uint32),
	}
}

// GetStoragePath returns the path to the application data folder
func GetStoragePath() (string, error) {
	appData, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}

	appFolder := filepath.Join(appData, appFolderName)
	if err := os.MkdirAll(appFolder, 0755); err != nil {
		return "", err
	}

	return filepath.Join(appFolder, stateFileName), nil
}

// LoadEmailState loads the email state from disk
func LoadEmailState() (*EmailState, error) {
	path, err := GetStoragePath()
	if err != nil {
		return nil, err
	}

	// If the file doesn't exist, return a new state
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return NewEmailState(), nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var state EmailState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}

	return &state, nil
}

// SaveEmailState saves the email state to disk using an atomic write operation.
func SaveEmailState(state *EmailState) error {
	path, err := GetStoragePath()
	if err != nil {
		return err
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}

	// Write to a temporary file first
	tempFile := path + ".tmp"
	if err := os.WriteFile(tempFile, data, 0644); err != nil {
		return err
	}

	// Rename the temporary file to the actual file (atomic operation)
	return os.Rename(tempFile, path)
}

// AddUID adds a UID to the list of last seen UIDs for a mailbox
// It keeps only the last 100 UIDs
func (s *EmailState) AddUID(mailbox string, uid uint32) {
	// Initialize slice if it doesn't exist
	if _, exists := s.LastUIDs[mailbox]; !exists {
		s.LastUIDs[mailbox] = []uint32{}
	}

	// Check if this UID is already in the list to avoid duplicates
	for _, existingUID := range s.LastUIDs[mailbox] {
		if existingUID == uid {
			return // UID already in the list, don't add it again
		}
	}

	// Add the UID to the list
	s.LastUIDs[mailbox] = append(s.LastUIDs[mailbox], uid)

	// Keep only the last 100 UIDs
	if len(s.LastUIDs[mailbox]) > 100 {
		s.LastUIDs[mailbox] = s.LastUIDs[mailbox][len(s.LastUIDs[mailbox])-100:]
	}
}

// GetLastUIDs returns the last seen UIDs for a mailbox
func (s *EmailState) GetLastUIDs(mailbox string) []uint32 {
	if uids, exists := s.LastUIDs[mailbox]; exists {
		return uids
	}
	return []uint32{}
}

// GetHighestUID returns the highest UID for a mailbox
func (s *EmailState) GetHighestUID(mailbox string) uint32 {
	uids := s.GetLastUIDs(mailbox)
	if len(uids) == 0 {
		return 0
	}

	// Find the highest UID
	var highest uint32
	for _, uid := range uids {
		if uid > highest {
			highest = uid
		}
	}
	return highest
}
