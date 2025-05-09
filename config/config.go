package config

// Config stores all application configuration
type Config struct {
	Email EmailConfig
}

// EmailConfig contains IMAP server and account settings
type EmailConfig struct {
	ImapServer    string
	ImapPort      int
	Username      string
	Password      string
	CheckInterval int // in seconds
}

// GetDefaultConfig returns the default configuration
func GetDefaultConfig() Config {
	return Config{
		Email: EmailConfig{
			ImapServer:    "",
			ImapPort:      993,
			Username:      "",
			Password:      "",
			CheckInterval: 60,
		},
	}
}
