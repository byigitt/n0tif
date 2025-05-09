package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/byigitt/n0tif/config"
	"github.com/kardianos/service"
)

// Define service configuration
var serviceConfig = &service.Config{
	Name:        "N0tifEmailService",
	DisplayName: "N0tif Email Notification Service",
	Description: "Checks for new emails and sends Windows notifications",
}

// Service struct to hold state
type n0tifService struct {
	emailCfg config.EmailConfig
	logger   service.Logger
}

// Start implements the service.Service interface
func (s *n0tifService) Start(svc service.Service) error {
	// Start should not block. Do the work in a goroutine.
	go s.run()
	return nil
}

// Stop implements the service.Service interface
func (s *n0tifService) Stop(svc service.Service) error {
	// Perform cleanup tasks if any
	log.Println("N0tif service stopping.")
	return nil
}

// run does the actual work of monitoring emails
func (s *n0tifService) run() {
	// The service is inherently a daemon, so pass true for daemonMode.
	// The EmailConfig is now directly available in s.emailCfg.
	log.Println("N0tif service run method executing runEmailMonitor.")
	runEmailMonitor(s.emailCfg)
}

// setupServiceLogging configures logging to go to both the service log and our custom log file
func setupServiceLogging(svc service.Service) {
	// Get service logger
	var err error
	_, err = svc.Logger(nil)
	if err != nil {
		log.Printf("Failed to get service logger: %v", err)
	}

	// Configure custom log file as well, this will be used by runEmailMonitor
	appDataDir := os.Getenv("APPDATA")
	logDir := filepath.Join(appDataDir, "n0tif")

	// Create directory if it doesn't exist
	if err := os.MkdirAll(logDir, 0755); err != nil {
		log.Printf("Failed to create log directory: %v", err)
		return
	}

	logFile := filepath.Join(logDir, "n0tif.log")
	f, err := os.OpenFile(logFile, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	if err != nil {
		log.Printf("Failed to open log file: %v", err)
		return
	}

	// Set standard log output to this file. This will be used by runEmailMonitor.
	log.SetOutput(f)
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)
	log.Println("Service logging configured to file.")
}

// runAsWindowsService attempts to run the program as a Windows service
// Takes resolved EmailConfig now
func runAsWindowsService(emailCfg config.EmailConfig, installAndStart bool, serviceArgs []string) {
	prg := &n0tifService{
		emailCfg: emailCfg,
	}
	svc, err := service.New(prg, serviceConfig)
	if err != nil {
		log.Fatalf("Failed to create service: %v", err)
	}

	// Setup logging. This needs to happen before Install/Start/Run calls
	// that might log through the service logger or our file logger.
	// Crucially, if the service runs, runEmailMonitor will use this logging setup.
	setupServiceLogging(svc)

	if installAndStart {
		// Attempt to control the service (install, start)
		// Check service.Control first if specific action like "install" is passed in serviceArgs
		if len(serviceArgs) > 0 {
			serviceAction := serviceArgs[0]
			if serviceAction == "install" || serviceAction == "uninstall" || serviceAction == "start" || serviceAction == "stop" {
				err := service.Control(svc, serviceAction)
				if err != nil {
					log.Fatalf("Failed to %s service: %v", serviceAction, err)
				}
				fmt.Printf("Service %s action successful.\n", serviceAction)
				return
			}
		}

		// Default install and start logic if no specific control action
		status, errStatus := svc.Status()
		if errStatus != nil { // Error means service is likely not installed
			log.Println("Service not found or status error, attempting to install...")
			if errInstall := svc.Install(); errInstall != nil {
				log.Fatalf("Failed to install service: %v", errInstall)
			}
			log.Println("Service installed successfully.")
			status = service.StatusStopped // Assume it's stopped after install
		}

		if status != service.StatusRunning {
			log.Println("Service not running, attempting to start...")
			if errStart := svc.Start(); errStart != nil {
				log.Fatalf("Failed to start service: %v", errStart)
			}
			log.Println("Service started successfully.")
		} else {
			log.Println("Service is already running.")
		}
		fmt.Println("N0tif service is configured and running.")
		fmt.Printf("Logs are at: %s\\n0tif\\n0tif.log\n", os.Getenv("APPDATA"))
		return
	}

	// If not installing/starting, just run the service (e.g., when SCM starts it)
	log.Println("Running service directly (e.g., started by SCM).")
	if errRun := svc.Run(); errRun != nil {
		log.Fatalf("Failed to run service: %v", errRun)
	}
}
