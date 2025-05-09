# N0tif - Email Notification Service

A simple Go application that checks your emails via IMAP and displays high-priority Windows notifications when new emails arrive.

## Features

- Periodically checks your IMAP email server for new emails
- Sends Windows toast notifications when new emails are detected
- Configurable check interval
- High-priority notifications with sound
- Stores email state between sessions (no duplicate notifications)
- Flexible execution modes: foreground, background, or Windows service
- Saves credentials securely for easy startup

## Installation

### Requirements

- Go 1.13 or higher
- Windows 10 or later (for toast notifications)

### Build from source

```bash
git clone https://github.com/byigitt/n0tif.git
cd n0tif
go build -o n0tif.exe cmd/n0tif/main.go cmd/n0tif/service.go
```

## Usage

### First-time setup with credential saving

To save your credentials for future use:

```
n0tif.exe -server imap.example.com -port 993 -user your.email@example.com -pass yourpassword -save
```

### Using saved credentials

After saving your credentials, you can simply run:

```
n0tif.exe
```

The application will automatically load your saved credentials (with password decrypted).

### Command-line flags

- `-server` - IMAP server address (required for first run)
- `-port` - IMAP server port (default: 993)
- `-user` - Email username/address (required for first run)
- `-pass` - Email password (required for first run)
- `-interval` - Check interval in seconds (default: 60)
- `-background` - Run in background mode (can be closed via Task Manager)
- `-service [action]` - Manage or run as a Windows service. Valid actions: `install`, `uninstall`, `start`, `stop`. If no action, installs and starts.
- `-save` - Save credentials for future use (password is encrypted)

### Running Modes

#### Foreground Mode (Default)

Running without any special flags:

```
n0tif.exe
```

In this mode, the program runs in the console window and can be terminated by pressing Ctrl+C.

#### Background Mode

To run the application in the background (without a console window):

```
n0tif.exe -background
```

When running in background mode:
- The program runs as a detached process 
- No console window is visible
- The process appears in Task Manager and can be easily terminated
- Logs are written to `%AppData%\n0tif\n0tif.log`

#### Windows Service Mode

To manage the application as a Windows service:

**Install the service:**
```
n0tif.exe -service install
```

**Start the service (after installation):**
```
n0tif.exe -service start
```

**Stop the service:**
```
n0tif.exe -service stop
```

**Uninstall the service:**
```
n0tif.exe -service uninstall
```

**Install and Start in one go (if not already installed/running):**
```
n0tif.exe -service
```
This command will install the service if it's not present, and then start it if it's not already running. 
If you provide credentials (e.g., `-server ... -user ... -pass ...`) along with `-service`, these will be used for the service configuration, especially useful for the first-time setup of the service.
If credentials are already saved (using `-save`), they will be used automatically.

When running as a Windows service:
- The program will continue running even after you log out
- It will automatically start when Windows starts (once installed and started)
- Logs will be written to `%AppData%\n0tif\n0tif.log`
- You can manage the service in Windows Services Manager (services.msc)
- The service is named "N0tifEmailService" in the services list

## Data Storage

N0tif stores data in the following locations:
- Email UIDs: `%AppData%\n0tif\email_state.json`
- Encrypted credentials: `%AppData%\n0tif\credentials.json`
- Log file: `%AppData%\n0tif\n0tif.log`

## Security

The application encrypts your email password using AES-256-GCM with a machine-specific key.
This means your password is stored securely and can only be decrypted on the same machine.

## Common IMAP Server Settings

### Gmail
- Server: imap.gmail.com
- Port: 993
- Note: You may need to enable "Less secure app access" or use an App Password

### Outlook/Hotmail
- Server: outlook.office365.com
- Port: 993

### Yahoo Mail
- Server: imap.mail.yahoo.com
- Port: 993

## Security Note

This application stores your email password in memory while running. 
It is recommended to use an app-specific password where possible rather than your main account password.

## License

MIT 