package storage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

const (
	appFolderName = "n0tif"
	stateFileName = "email_state.json"
)

// EmailState stores information about previously seen emails
type EmailState struct {
	LastSeenDates map[string]time.Time `json:"last_seen_dates"` // Maps mailbox to the InternalDate of the last seen email
}

// NewEmailState creates a new email state
func NewEmailState() *EmailState {
	return &EmailState{
		LastSeenDates: make(map[string]time.Time),
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

// UpdateLastSeenDate updates the last seen date for a mailbox.
// It only updates if the new date is later than the currently stored date.
func (s *EmailState) UpdateLastSeenDate(mailbox string, date time.Time) {
	if currentDate, exists := s.LastSeenDates[mailbox]; !exists || date.After(currentDate) {
		s.LastSeenDates[mailbox] = date
	}
}

// GetLastSeenDate returns the last seen date for a mailbox.
// Returns a zero time.Time if no date is stored for the mailbox.
func (s *EmailState) GetLastSeenDate(mailbox string) time.Time {
	if date, exists := s.LastSeenDates[mailbox]; exists {
		return date
	}
	return time.Time{} // Return zero time if not found
}
