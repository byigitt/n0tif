package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/byigitt/n0tif/config"
	"github.com/byigitt/n0tif/internal/email"
	"github.com/byigitt/n0tif/internal/notify"
	"github.com/byigitt/n0tif/internal/storage"
)

// Global flags for application configuration
var (
	imapServer  = flag.String("server", "", "IMAP server address")
	imapPort    = flag.Int("port", 993, "IMAP server port")
	username    = flag.String("user", "", "Email username/address")
	password    = flag.String("pass", "", "Email password")
	interval    = flag.Int("interval", 60, "Check interval in seconds")
	save        = flag.Bool("save", false, "Save credentials for future use")
	background  = flag.Bool("background", false, "Run in background (can be closed via Task Manager)")
	serviceMode = flag.Bool("service", false, "Install and run as Windows service (auto-starts with Windows)")
	isDaemon    = flag.Bool("daemon", false, "Internal use: Indicates process is a daemon child")
)

func main() {
	flag.Parse() // Parse all flags once at the beginning

	if *isDaemon {
		// If this is a daemon child, its stdout/stderr might be nil (set by parent).
		// setupFileLoggingAndExitOnFailure will attempt to redirect log.* to a file.
		// If it fails, it writes an emergency log and exits.
		setupFileLoggingAndExitOnFailure()
		log.Println("N0tif daemon process initialised with file logging.")
	}

	appCfgEmail := loadAppConfig() // Centralized config loading, uses global parsed flags

	if *serviceMode {
		// service.go's runAsWindowsService handles its own logging via setupServiceLogging (which also sets log.SetOutput).
		// The 'true' here is for the installAndStart parameter in runAsWindowsService.
		runAsWindowsService(appCfgEmail, true, os.Args[1:])
		return
	}

	if *background {
		runInBackground(appCfgEmail) // Pass fully resolved config
		return
	}

	// Foreground execution
	if !*isDaemon { // Only print this if truly foreground, not a -daemon child being run directly for testing
		log.Println("Starting N0tif - Email Notification Service (Foreground)")
	}
	runEmailMonitor(appCfgEmail)
}

// loadAppConfig resolves the email configuration from flags or storage.
// It uses the globally parsed flags.
// It will log.Fatal if essential configuration is missing and not loadable.
func loadAppConfig() config.EmailConfig {
	cfg := config.GetDefaultConfig() // Start with defaults

	// Check if essential credential flags were explicitly set by the user on the command line.
	// A simple check is if they are different from their zero/default values after flag.Parse().
	// More robust: use flag.Visit to see which flags were actually set.
	// For now, we assume if they are non-empty/non-default, they were set.
	hasExplicitServer := *imapServer != ""
	hasExplicitUser := *username != ""
	hasExplicitPass := *password != ""
	// For port and interval, we can check if they differ from default if needed, or assume if primary creds are set, these are also intended.

	usingSavedCreds := false
	// If no primary credential flags were set, try to load from storage.
	if !hasExplicitServer && !hasExplicitUser && !hasExplicitPass {
		if storage.CredentialsExist() {
			log.Println("No explicit credentials provided via flags, attempting to load saved credentials...")
			savedCfg, err := storage.LoadCredentials()
			if err != nil {
				log.Fatalf("Failed to load saved credentials: %v. Please provide credentials or use -save.", err)
			}
			cfg.Email = *savedCfg
			usingSavedCreds = true
			log.Printf("Loaded credentials for %s on server %s", savedCfg.Username, savedCfg.ImapServer)
		} else {
			// If this is a daemon child, it MUST have received explicit args from its parent (runInBackground).
			// So if it reaches here, something is wrong with how it was launched or parsed its args.
			if *isDaemon {
				log.Fatal("CRITICAL_DAEMON_CONFIG_ERROR: Daemon started without necessary credential arguments and no saved credentials found. This indicates an issue with parent process argument passing.")
			} else {
				log.Fatal("No credentials provided and no saved credentials found. Required flags: -server, -user, -pass, or use -save.")
			}
		}
	} else {
		// Use explicitly provided flags if they were set
		// This part assumes that if any of server/user/pass is set, all required ones should be set.
		if hasExplicitServer {
			cfg.Email.ImapServer = *imapServer
		}
		if *imapPort != config.GetDefaultConfig().Email.ImapPort {
			cfg.Email.ImapPort = *imapPort
		}
		if hasExplicitUser {
			cfg.Email.Username = *username
		}
		if hasExplicitPass {
			cfg.Email.Password = *password
		}
		if *interval != config.GetDefaultConfig().Email.CheckInterval {
			cfg.Email.CheckInterval = *interval
		}
	}

	// Final validation for all paths
	if cfg.Email.ImapServer == "" || cfg.Email.Username == "" || cfg.Email.Password == "" {
		log.Fatal("Missing required email configuration: server, username, and password are required.")
	}

	// Save credentials if -save flag is present AND we are using explicitly provided flags (not loaded ones).
	if *save && (hasExplicitServer || hasExplicitUser || hasExplicitPass) && !usingSavedCreds {
		log.Println("Saving provided credentials...")
		if err := storage.SaveCredentials(cfg.Email); err != nil {
			log.Printf("Warning: Failed to save credentials: %v", err)
		} else {
			log.Println("Credentials saved successfully.")
		}
	}
	return cfg.Email
}

// runEmailMonitor contains the main logic. Assumes logging is pre-configured.
func runEmailMonitor(emailCfg config.EmailConfig) {
	log.Println("runEmailMonitor: Initializing with loaded/parsed config.")
	imapChecker, err := email.NewImapChecker(emailCfg)
	if err != nil {
		log.Fatalf("Failed to initialize email checker: %v", err)
	}

	log.Println("Initializing email tracking...")
	if err := imapChecker.InitializeEmailTracking(); err != nil {
		log.Printf("Warning: Failed to initialize email tracking: %v", err)
	} else {
		log.Println("Email tracking initialized successfully.")
	}

	handleNewEmails := func(subjects []string) {
		if len(subjects) == 0 {
			return
		}
		notificationTitle := "New Email"
		notificationMessage := fmt.Sprintf("You have a new email: %s", subjects[0])
		if len(subjects) > 1 {
			notificationTitle = "New Emails"
			notificationMessage = fmt.Sprintf("You have %d new emails", len(subjects))
		}
		if errNotify := notify.SendWindowsNotification(notificationTitle, notificationMessage, true); errNotify != nil {
			log.Printf("Failed to send notification: %v", errNotify)
		}
	}

	imapChecker.StartChecking(handleNewEmails)
	log.Printf("Email checker started for %s. Checking every %d seconds.", emailCfg.Username, emailCfg.CheckInterval)

	// Create a signal channel to keep the process alive indefinitely
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Keep the daemon process alive explicitly
	if *isDaemon {
		log.Println("Daemon process is now running indefinitely.")
		// Block indefinitely until a signal is received
		<-sigChan
	} else {
		// For foreground mode, just wait for signals
		<-sigChan
	}

	log.Println("Shutting down...")
}

// runInBackground relaunches the application as a background (detached) process.
func runInBackground(emailCfg config.EmailConfig) {
	exePath, err := os.Executable()
	if err != nil {
		log.Fatalf("Failed to get executable path: %v", err)
	}

	// Create necessary directory for logs
	configDir, err := os.UserConfigDir()
	if err != nil {
		log.Fatalf("Failed to get user config directory: %v", err)
	}

	logDir := filepath.Join(configDir, "n0tif")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		log.Fatalf("Failed to create log directory: %v", err)
	}

	args := []string{
		"-daemon",
		"-server", emailCfg.ImapServer,
		"-port", strconv.Itoa(emailCfg.ImapPort),
		"-user", emailCfg.Username,
		"-pass", emailCfg.Password,
		"-interval", strconv.Itoa(emailCfg.CheckInterval),
	}

	cmd := exec.Command(exePath, args...)

	// Don't redirect stdout/stderr to nil, as this may cause issues with the process
	// Instead, create a log file and redirect to it directly
	logFile := filepath.Join(logDir, "n0tif-daemon-init.log")
	f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		log.Fatalf("Failed to create initial daemon log file: %v", err)
	}
	defer f.Close()

	cmd.Stdout = f
	cmd.Stderr = f

	// For Windows, use CREATE_NEW_PROCESS_GROUP to detach, but not DETACHED_PROCESS
	// This combination should allow the console window to be hidden but the process to stay alive
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}

	if err := cmd.Start(); err != nil {
		log.Fatalf("Failed to start background process: %v", err)
	}

	// Wait briefly for the process to start and create its logs
	time.Sleep(500 * time.Millisecond)

	// Check if the process is still running
	if cmd.Process == nil {
		log.Fatalf("Failed to start background process: Process object is nil")
	}

	// Verify the process exists (will error if not found)
	_, err = os.FindProcess(cmd.Process.Pid)
	if err != nil {
		log.Fatalf("Failed to find started process: %v", err)
	}

	// Print startup success message
	fmt.Printf("N0tif has been started in the background (PID: %d)\n", cmd.Process.Pid)
	fmt.Printf("You can close it from Task Manager using this PID.\n")
	logPath := filepath.Join(configDir, "n0tif", "n0tif.log")
	fmt.Printf("Logs can be found at: %s\n", logPath)
	os.Exit(0)
}

// writeEmergencyLog writes to a predefined temporary file if primary logging setup fails.
// This is a last resort for daemon processes where stderr might be nil.
func writeEmergencyLog(message string) {
	emergencyLogPath := filepath.Join(os.TempDir(), "n0tif_daemon_startup_critical_error.txt")
	// Append timestamp to message to distinguish multiple errors
	fullMessage := fmt.Sprintf("%s: %s\n", time.Now().Format(time.RFC3339Nano), message)
	// Best effort to write the emergency log.
	// Use O_APPEND and O_CREATE in case of multiple quick failures or existing file.
	f, err := os.OpenFile(emergencyLogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err == nil {
		defer f.Close()
		_, _ = f.WriteString(fullMessage)
	}
	// If opening/writing emergency log fails, there's not much more we can do silently.
}

// setupFileLoggingAndExitOnFailure configures file logging.
// It is called very early in main() if the -daemon flag is set.
// If it fails, it calls writeEmergencyLog and then os.Exit(1).
func setupFileLoggingAndExitOnFailure() {
	configDir, err := os.UserConfigDir()
	if err != nil {
		errMsg := fmt.Sprintf("CRITICAL_ERROR: Failed to get user config directory for logging: %v", err)
		writeEmergencyLog(errMsg)
		os.Exit(1)
	}
	logDir := filepath.Join(configDir, "n0tif")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		errMsg := fmt.Sprintf("CRITICAL_ERROR: Failed to create log directory '%s': %v", logDir, err)
		writeEmergencyLog(errMsg)
		os.Exit(1)
	}
	logFile := filepath.Join(logDir, "n0tif.log")

	f, err := os.OpenFile(logFile, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	if err != nil {
		errMsg := fmt.Sprintf("CRITICAL_ERROR: Failed to open log file '%s': %v", logFile, err)
		writeEmergencyLog(errMsg)
		os.Exit(1)
	}

	log.SetOutput(f)
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)
	// Do not return 'f' as we are not redirecting stdout/stderr with Dup2 anymore.
	// The file will be implicitly closed on process exit or if logger is reconfigured.
}
