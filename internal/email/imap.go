package email

import (
	"fmt"
	"log"
	"sort" // For sorting UIDs for consistent logging
	"time"

	"github.com/byigitt/n0tif/config"
	"github.com/byigitt/n0tif/internal/storage"
	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
)

const mailboxName = "INBOX" // Define as a constant

// ImapChecker handles checking for new emails
type ImapChecker struct {
	config       config.EmailConfig
	emailState   *storage.EmailState
	notifiedUIDs map[uint32]bool
}

// NewImapChecker creates a new IMAP email checker
func NewImapChecker(cfg config.EmailConfig) (*ImapChecker, error) {
	state, err := storage.LoadEmailState()
	if err != nil {
		return nil, fmt.Errorf("failed to load email state: %w", err)
	}

	notified := make(map[uint32]bool)
	loadedUIDs := state.GetLastUIDs(mailboxName)
	for _, uid := range loadedUIDs {
		notified[uid] = true
	}
	log.Printf("NewImapChecker: Loaded %d UIDs into notifiedUIDs from storage: %v", len(loadedUIDs), loadedUIDs)

	return &ImapChecker{
		config:       cfg,
		emailState:   state,
		notifiedUIDs: notified,
	}, nil
}

func (ic *ImapChecker) saveStateWithLogging(operationDesc string) {
	log.Printf("saveStateWithLogging (%s): Current UIDs in emailState for %s before save: %v", operationDesc, mailboxName, ic.emailState.GetLastUIDs(mailboxName))
	if err := storage.SaveEmailState(ic.emailState); err != nil {
		log.Printf("saveStateWithLogging (%s): WARNING - Failed to save email state: %v", operationDesc, err)
	} else {
		log.Printf("saveStateWithLogging (%s): Email state saved successfully.", operationDesc)
	}
	log.Printf("saveStateWithLogging (%s): Current UIDs in emailState for %s after save attempt: %v", operationDesc, mailboxName, ic.emailState.GetLastUIDs(mailboxName))
}

func (ic *ImapChecker) InitializeEmailTracking() error {
	if len(ic.notifiedUIDs) > 0 {
		uids := make([]uint32, 0, len(ic.notifiedUIDs))
		for uid := range ic.notifiedUIDs {
			uids = append(uids, uid)
		}
		sort.Slice(uids, func(i, j int) bool { return uids[i] < uids[j] })
		log.Printf("InitializeEmailTracking: Using existing state. %d UIDs already marked as notified: %v", len(uids), uids)

		// Log the highest UID for debugging
		var highestUID uint32
		for _, uid := range uids {
			if uid > highestUID {
				highestUID = uid
			}
		}
		log.Printf("InitializeEmailTracking: Highest known UID from state: %d", highestUID)
		return nil
	}

	log.Println("InitializeEmailTracking: No existing state or empty state loaded. Establishing new baseline...")

	c, err := ic.connect()
	if err != nil {
		return fmt.Errorf("InitializeEmailTracking connect: %w", err)
	}
	defer c.Logout()

	mbox, err := c.Select(mailboxName, false)
	if err != nil {
		return fmt.Errorf("InitializeEmailTracking select mailbox: %w", err)
	}

	if mbox.Messages == 0 {
		log.Println("InitializeEmailTracking: No messages in INBOX to initialize baseline from.")
		ic.saveStateWithLogging("InitializeEmailTracking - no messages")
		return nil
	}

	// Check UIDNEXT to understand the latest UID pattern
	log.Printf("InitializeEmailTracking: Mailbox info - Messages: %d, Recent: %d, Unseen: %d, UIDNext: %d, UIDValidity: %d",
		mbox.Messages, mbox.Recent, mbox.Unseen, mbox.UidNext, mbox.UidValidity)

	numToFetch := uint32(10) // Increased from 5 to 10 to get a better sample
	if mbox.Messages < numToFetch {
		numToFetch = mbox.Messages
	}
	from := mbox.Messages - numToFetch + 1

	seqSetForInit := new(imap.SeqSet) // Renamed to avoid conflict if we re-introduce for fallback
	seqSetForInit.AddRange(from, mbox.Messages)

	// Request internal date for proper sorting
	items := []imap.FetchItem{imap.FetchUid, imap.FetchEnvelope, imap.FetchInternalDate}
	messagesChan := make(chan *imap.Message, numToFetch)

	log.Printf("InitializeEmailTracking: Fetching messages %d-%d to establish baseline.", from, mbox.Messages)
	done := make(chan error, 1)
	go func() {
		done <- c.Fetch(seqSetForInit, items, messagesChan)
	}()

	// Collect messages to sort by date
	type EmailInfo struct {
		UID     uint32
		Date    time.Time
		Subject string
		SeqNum  uint32
	}
	var emails []EmailInfo

	for msg := range messagesChan {
		emails = append(emails, EmailInfo{
			UID:     msg.Uid,
			Date:    msg.InternalDate,
			Subject: msg.Envelope.Subject,
			SeqNum:  msg.SeqNum,
		})
		log.Printf("InitializeEmailTracking: Collected email - SeqNum: %d, UID: %d, Date: %s, Subject: '%s'",
			msg.SeqNum, msg.Uid, msg.InternalDate.Format(time.RFC3339), msg.Envelope.Subject)
	}

	if err := <-done; err != nil {
		return fmt.Errorf("InitializeEmailTracking fetch messages: %w", err)
	}

	if len(emails) == 0 {
		log.Println("InitializeEmailTracking: No messages fetched for baseline.")
		return nil
	}

	// Check if UIDs are in proper ascending order
	var prevUID uint32
	uidsInOrder := true
	for i, email := range emails {
		if i > 0 && email.UID < prevUID {
			uidsInOrder = false
			log.Printf("WARNING: UIDs not in ascending order! Email %d has UID %d which is less than previous email's UID %d",
				i, email.UID, prevUID)
		}
		prevUID = email.UID
	}

	if !uidsInOrder {
		log.Println("WARNING: UIDs are not in proper ascending order! This may cause notification issues.")
	}

	// Sort emails by date - newest first
	sort.Slice(emails, func(i, j int) bool {
		return emails[i].Date.After(emails[j].Date)
	})

	// Store the most recent emails as our baseline
	baselineUIDsAdded := 0
	log.Println("InitializeEmailTracking: Adding baseline emails (sorted by date, newest first):")
	for i, email := range emails {
		ic.emailState.AddUID(mailboxName, email.UID)
		// Track these UIDs so we don't notify for them
		ic.notifiedUIDs[email.UID] = true
		baselineUIDsAdded++
		log.Printf("InitializeEmailTracking: Added baseline #%d: UID %d (SeqNum: %d, Subject: '%s', Date: %s)",
			i+1, email.UID, email.SeqNum, email.Subject, email.Date.Format(time.RFC3339))
	}

	if baselineUIDsAdded > 0 {
		ic.saveStateWithLogging(fmt.Sprintf("InitializeEmailTracking - %d baseline UIDs added", baselineUIDsAdded))
	}

	log.Printf("InitializeEmailTracking: Baseline established with %d UIDs (sorted by date, newest first).", baselineUIDsAdded)
	return nil
}

func (ic *ImapChecker) connect() (*client.Client, error) {
	serverAddr := fmt.Sprintf("%s:%d", ic.config.ImapServer, ic.config.ImapPort)
	c, err := client.DialTLS(serverAddr, nil)
	if err != nil {
		return nil, fmt.Errorf("connect DialTLS: %w", err)
	}
	if err := c.Login(ic.config.Username, ic.config.Password); err != nil {
		c.Logout()
		return nil, fmt.Errorf("connect Login: %w", err)
	}
	return c, nil
}

func (ic *ImapChecker) CheckForNewEmails() ([]string, error) {
	log.Println("CheckForNewEmails: Starting check...")
	newEmailSubjects := []string{}
	stateChanged := false

	c, err := ic.connect()
	if err != nil {
		return nil, err
	}
	defer c.Logout()

	mbox, err := c.Select(mailboxName, false)
	if err != nil {
		return nil, fmt.Errorf("CheckForNewEmails select mailbox: %w", err)
	}

	if mbox.Messages == 0 {
		log.Println("CheckForNewEmails: No messages in INBOX.")
		return newEmailSubjects, nil
	}

	// Get all known UIDs from state
	knownUIDs := ic.emailState.GetLastUIDs(mailboxName)
	var highestKnownUID uint32
	for _, uid := range knownUIDs {
		if uid > highestKnownUID {
			highestKnownUID = uid
		}
	}
	log.Printf("CheckForNewEmails: Highest known UID from state for %s: %d", mailboxName, highestKnownUID)

	// If no highest UID is known, we should initialize the baseline instead of checking for new emails
	if highestKnownUID == 0 && mbox.Messages > 0 {
		log.Printf("CheckForNewEmails: No highest UID known, but %d messages exist. Initializing baseline instead.", mbox.Messages)
		if err := ic.InitializeEmailTracking(); err != nil {
			return nil, fmt.Errorf("failed to initialize email tracking: %w", err)
		}
		return newEmailSubjects, nil
	}

	// Check recently arrived emails
	// Instead of using UID search which can be unreliable on some servers,
	// we'll use the sequence number approach to get recent emails
	numToCheck := uint32(30) // Check last 30 emails to find new ones
	if mbox.Messages < numToCheck {
		numToCheck = mbox.Messages
	}

	from := uint32(1)
	if mbox.Messages > numToCheck {
		from = mbox.Messages - numToCheck + 1
	}

	seqSet := new(imap.SeqSet)
	seqSet.AddRange(from, mbox.Messages)

	log.Printf("CheckForNewEmails: Fetching last %d emails (sequence numbers %d-%d) to check for new ones",
		numToCheck, from, mbox.Messages)

	items := []imap.FetchItem{imap.FetchUid, imap.FetchEnvelope, imap.FetchInternalDate}
	messagesChan := make(chan *imap.Message, numToCheck)
	done := make(chan error, 1)

	go func() {
		done <- c.Fetch(seqSet, items, messagesChan)
	}()

	// Collect emails to process
	type EmailWithDate struct {
		Subject string
		Date    time.Time
		UID     uint32
	}
	var newEmails []EmailWithDate
	var processedUIDs []uint32

	for msg := range messagesChan {
		processedUIDs = append(processedUIDs, msg.Uid)

		// If we haven't notified for this UID yet, process it as new
		if !ic.notifiedUIDs[msg.Uid] {
			log.Printf("CheckForNewEmails: New email UID %d (Subject: '%s', Date: %s) - adding to notifications.",
				msg.Uid, msg.Envelope.Subject, msg.InternalDate.Format(time.RFC3339))

			newEmails = append(newEmails, EmailWithDate{
				Subject: msg.Envelope.Subject,
				Date:    msg.InternalDate,
				UID:     msg.Uid,
			})

			// Mark as notified and add to state
			ic.notifiedUIDs[msg.Uid] = true
			ic.emailState.AddUID(mailboxName, msg.Uid)
			stateChanged = true
		}
	}

	if err := <-done; err != nil {
		return nil, fmt.Errorf("CheckForNewEmails fetch messages: %w", err)
	}

	if len(processedUIDs) > 0 {
		sort.Slice(processedUIDs, func(i, j int) bool { return processedUIDs[i] < processedUIDs[j] })
		log.Printf("CheckForNewEmails: Processed %d UIDs ranging from %d to %d",
			len(processedUIDs), processedUIDs[0], processedUIDs[len(processedUIDs)-1])
	}

	// Sort new emails by date, most recent first
	sort.Slice(newEmails, func(i, j int) bool {
		return newEmails[i].Date.After(newEmails[j].Date)
	})

	// Create the final sorted subject list
	for _, email := range newEmails {
		newEmailSubjects = append(newEmailSubjects, email.Subject)
	}

	if stateChanged {
		ic.saveStateWithLogging("CheckForNewEmails - new UIDs processed")
	}

	log.Printf("CheckForNewEmails: Finished check. Returning %d new email subjects (sorted by date, newest first).", len(newEmailSubjects))
	return newEmailSubjects, nil
}

func (ic *ImapChecker) StartChecking(callback func([]string)) {
	go func() {
		log.Println("StartChecking: Performing initial email check...")
		newEmails, err := ic.CheckForNewEmails()
		if err != nil {
			log.Printf("StartChecking: Error during initial email check: %v", err)
		} else if len(newEmails) > 0 {
			log.Printf("StartChecking: Found %d new emails on initial check.", len(newEmails))
			callback(newEmails)
		} else {
			log.Println("StartChecking: No new emails found on initial check.")
		}

		ticker := time.NewTicker(time.Duration(ic.config.CheckInterval) * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			log.Println("StartChecking: Scheduled email check...")
			newEmails, err := ic.CheckForNewEmails()
			if err != nil {
				log.Printf("StartChecking: Error checking emails: %v", err)
				continue
			}

			if len(newEmails) > 0 {
				log.Printf("StartChecking: Found %d new emails.", len(newEmails))
				callback(newEmails)
			}
		}
	}()
}

// ResetState clears all tracked email UIDs for debugging
func (ic *ImapChecker) ResetState() {
	// Clear the map of notified UIDs
	ic.notifiedUIDs = make(map[uint32]bool)

	// Reset the email state object
	ic.emailState = storage.NewEmailState()

	// Save the empty state
	ic.saveStateWithLogging("ResetState - clearing all tracked UIDs")

	// Reinitialize tracking with current emails
	err := ic.InitializeEmailTracking()
	if err != nil {
		log.Printf("Warning: Failed to initialize email tracking after reset: %v", err)
	} else {
		log.Println("Email tracking re-initialized successfully after reset.")
	}
}
