package storage

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/byigitt/n0tif/config"
)

const (
	credsFileName = "credentials.json"
)

// Credentials stores encrypted email credentials
type Credentials struct {
	ImapServer    string `json:"imap_server"`
	ImapPort      int    `json:"imap_port"`
	Username      string `json:"username"`
	Password      string `json:"password"` // Encrypted password
	CheckInterval int    `json:"check_interval"`
}

// GetCredentialsPath returns the path to the credentials file
func GetCredentialsPath() (string, error) {
	appData, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}

	appFolder := filepath.Join(appData, appFolderName)
	if err := os.MkdirAll(appFolder, 0755); err != nil {
		return "", err
	}

	return filepath.Join(appFolder, credsFileName), nil
}

// SaveCredentials encrypts and saves the email credentials to disk using an atomic write operation.
func SaveCredentials(cfg config.EmailConfig) error {
	// Encrypt password
	encryptedPass, err := encryptPassword(cfg.Password)
	if err != nil {
		return err
	}

	creds := Credentials{
		ImapServer:    cfg.ImapServer,
		ImapPort:      cfg.ImapPort,
		Username:      cfg.Username,
		Password:      encryptedPass,
		CheckInterval: cfg.CheckInterval,
	}

	// Convert to JSON
	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return err
	}

	// Save to file
	path, err := GetCredentialsPath()
	if err != nil {
		return err
	}

	// Write to a temporary file first
	tempFile := path + ".tmp"
	if err := os.WriteFile(tempFile, data, 0600); err != nil { // Use same restrictive permissions
		return err
	}

	// Rename the temporary file to the actual file (atomic operation)
	return os.Rename(tempFile, path)
}

// LoadCredentials loads and decrypts the email credentials from disk
func LoadCredentials() (*config.EmailConfig, error) {
	path, err := GetCredentialsPath()
	if err != nil {
		return nil, err
	}

	// If the file doesn't exist, return error
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, errors.New("no saved credentials found")
	}

	// Read the file
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	// Parse JSON
	var creds Credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, err
	}

	// Decrypt password
	decryptedPass, err := decryptPassword(creds.Password)
	if err != nil {
		return nil, err
	}

	// Return config
	return &config.EmailConfig{
		ImapServer:    creds.ImapServer,
		ImapPort:      creds.ImapPort,
		Username:      creds.Username,
		Password:      decryptedPass,
		CheckInterval: creds.CheckInterval,
	}, nil
}

// CredentialsExist checks if credentials file exists
func CredentialsExist() bool {
	path, err := GetCredentialsPath()
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return !os.IsNotExist(err)
}

// generateEncryptionKey derives an encryption key from the machine-specific information
func generateEncryptionKey() []byte {
	// Use machine-specific values to create a stable key
	hostname, _ := os.Hostname()
	username := os.Getenv("USERNAME") // Windows username

	// Create a hash using these values
	hasher := sha256.New()
	hasher.Write([]byte(hostname))
	hasher.Write([]byte(username))
	hasher.Write([]byte("n0tif-secret-key")) // Add a constant salt

	return hasher.Sum(nil)
}

// encryptPassword encrypts the password using machine-specific encryption
func encryptPassword(password string) (string, error) {
	key := generateEncryptionKey()
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}

	// Create a new GCM cipher
	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	// Create a nonce
	nonce := make([]byte, aesGCM.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}

	// Encrypt the password
	ciphertext := aesGCM.Seal(nonce, nonce, []byte(password), nil)

	// Return as hex string
	return hex.EncodeToString(ciphertext), nil
}

// decryptPassword decrypts the password using machine-specific decryption
func decryptPassword(encryptedPassword string) (string, error) {
	key := generateEncryptionKey()
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}

	// Create a new GCM cipher
	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	// Decode hex string
	ciphertext, err := hex.DecodeString(encryptedPassword)
	if err != nil {
		return "", err
	}

	// Get the nonce size
	nonceSize := aesGCM.NonceSize()
	if len(ciphertext) < nonceSize {
		return "", errors.New("ciphertext too short")
	}

	// Extract nonce and ciphertext
	nonce, ciphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]

	// Decrypt
	plaintext, err := aesGCM.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}

	return string(plaintext), nil
}
