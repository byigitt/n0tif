package notify

import (
	"github.com/go-toast/toast"
)

// SendWindowsNotification sends a high priority Windows toast notification
func SendWindowsNotification(title, message string, isHighPriority bool) error {
	notification := toast.Notification{
		AppID:   "N0tif Email Alert",
		Title:   title,
		Message: message,
		Actions: []toast.Action{
			{Type: "protocol", Label: "Open Email Client", Arguments: "mailto:"},
		},
	}

	// Set high priority options if requested
	if isHighPriority {
		notification.ActivationType = "protocol"
		notification.Duration = "long"
		notification.Audio = toast.Mail
		notification.Loop = false
	}

	return notification.Push()
}
