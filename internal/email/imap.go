package email

import (
	"fmt"
	"log"
	"sort"
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
	lastSeenDate time.Time // Date of the last email processed
}

// NewImapChecker creates a new IMAP email checker
func NewImapChecker(cfg config.EmailConfig) (*ImapChecker, error) {
	state, err := storage.LoadEmailState()
	if err != nil {
		return nil, fmt.Errorf("failed to load email state: %w", err)
	}

	lastDate := state.GetLastSeenDate(mailboxName)
	log.Printf("NewImapChecker: Loaded lastSeenDate from storage: %s", lastDate.Format(time.RFC3339))

	return &ImapChecker{
		config:       cfg,
		emailState:   state,
		lastSeenDate: lastDate,
	}, nil
}

func (ic *ImapChecker) saveStateWithLogging(operationDesc string) {
	// Update the state object before saving
	ic.emailState.UpdateLastSeenDate(mailboxName, ic.lastSeenDate)
	log.Printf("saveStateWithLogging (%s): Current lastSeenDate for %s before save: %s", operationDesc, mailboxName, ic.lastSeenDate.Format(time.RFC3339))
	if err := storage.SaveEmailState(ic.emailState); err != nil {
		log.Printf("saveStateWithLogging (%s): WARNING - Failed to save email state: %v", operationDesc, err)
	} else {
		log.Printf("saveStateWithLogging (%s): Email state (lastSeenDate: %s) saved successfully.", operationDesc, ic.lastSeenDate.Format(time.RFC3339))
	}
}

func (ic *ImapChecker) InitializeEmailTracking() error {
	if !ic.lastSeenDate.IsZero() {
		log.Printf("InitializeEmailTracking: Using existing lastSeenDate from state: %s", ic.lastSeenDate.Format(time.RFC3339))
		return nil
	}

	log.Println("InitializeEmailTracking: No existing lastSeenDate. Establishing new baseline by fetching the most recent email...")

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
		// lastSeenDate remains zero, will be saved as such if saveStateWithLogging is called.
		// Or, we can explicitly save a zero date to mark it as checked.
		ic.saveStateWithLogging("InitializeEmailTracking - no messages, setting zero date")
		return nil
	}

	// Fetch only the very last message to set the baseline
	// Sequence numbers are 1-based.
	seqSet := new(imap.SeqSet)
	seqSet.AddNum(mbox.Messages) // Fetch only the last message by sequence number

	items := []imap.FetchItem{imap.FetchEnvelope, imap.FetchInternalDate, imap.FetchUid} // UID for logging
	messagesChan := make(chan *imap.Message, 1)

	log.Printf("InitializeEmailTracking: Fetching the last message (SeqNum: %d) to establish baseline date.", mbox.Messages)
	if err := c.Fetch(seqSet, items, messagesChan); err != nil {
		return fmt.Errorf("InitializeEmailTracking fetch last message: %w", err)
	}

	// msg := <-messagesChan // This would block if fetch had an error and didn't send.
	// Safer to range, though we expect only one or zero messages.
	var newestMessage *imap.Message
	for msg := range messagesChan { // Loop will run once if a message is fetched
		newestMessage = msg
	}

	if newestMessage == nil {
		log.Println("InitializeEmailTracking: No message found when fetching the last message. This is unexpected if mbox.Messages > 0.")
		// Proceed with zero date, will be saved.
		ic.saveStateWithLogging("InitializeEmailTracking - last message fetch failed")
		return nil
	}

	ic.lastSeenDate = newestMessage.InternalDate
	log.Printf("InitializeEmailTracking: Baseline established. LastSeenDate set to: %s (from email UID: %d, Subject: '%s')",
		ic.lastSeenDate.Format(time.RFC3339), newestMessage.Uid, newestMessage.Envelope.Subject)

	ic.saveStateWithLogging(fmt.Sprintf("InitializeEmailTracking - baseline date %s set", ic.lastSeenDate.Format(time.RFC3339)))
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
	stateChanged := false // To track if lastSeenDate is updated

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

	// If lastSeenDate is zero, it means we haven't initialized yet or state was reset.
	if ic.lastSeenDate.IsZero() {
		log.Println("CheckForNewEmails: lastSeenDate is zero. Initializing email tracking first.")
		if initErr := ic.InitializeEmailTracking(); initErr != nil {
			return nil, fmt.Errorf("CheckForNewEmails: failed to initialize email tracking: %w", initErr)
		}
		// After initialization, lastSeenDate might still be zero if inbox was empty.
		// In this case, proceed with the current (potentially still zero) lastSeenDate.
		log.Printf("CheckForNewEmails: Initialization complete. Current lastSeenDate: %s", ic.lastSeenDate.Format(time.RFC3339))
	}

	criteria := imap.NewSearchCriteria()
	// If lastSeenDate is not zero, search for emails SINCE that date.
	// The SINCE command is usually exclusive of the date itself, but server behavior can vary.
	// We will ensure to only process emails strictly AFTER lastSeenDate.
	if !ic.lastSeenDate.IsZero() {
		criteria.Since = ic.lastSeenDate
		log.Printf("CheckForNewEmails: Searching for emails SINCE %s", ic.lastSeenDate.Format(time.RFC3339))
	} else {
		// If lastSeenDate is still zero (e.g., first run, empty inbox during init),
		// fetch all messages or a recent subset to avoid overwhelming results.
		// For simplicity, let's try to fetch all. If this is too much, we can limit it.
		// An empty criteria.SINCE means all messages since epoch, essentially.
		// Alternatively, use criteria.All = true, but an empty criteria usually means all.
		log.Println("CheckForNewEmails: lastSeenDate is zero, attempting to search for all messages (or recent ones if server limits).")
		// To be safe and avoid fetching thousands of emails on a very old mailbox first run,
		// let's fetch the last N (e.g., 50) if lastSeenDate is zero.
		// This requires fetching by sequence numbers first, then filtering.
		// For now, let's proceed with SINCE (which will be SINCE epoch if date is zero).
		// The user accepted potential misses, so a broad SINCE might be okay.
		// If not, we'd fetch recent sequence numbers and then filter by date.
		// Let's assume a `SINCE zero-date` will effectively give us recent items or all.
	}

	seqNums, err := c.Search(criteria)
	if err != nil {
		return nil, fmt.Errorf("CheckForNewEmails search: %w", err)
	}

	if len(seqNums) == 0 {
		log.Println("CheckForNewEmails: No messages found matching search criteria.")
		return newEmailSubjects, nil
	}
	log.Printf("CheckForNewEmails: Found %d messages matching search criteria. SeqNums: %v", len(seqNums), seqNums)

	seqSet := new(imap.SeqSet)
	seqSet.AddNum(seqNums...)

	items := []imap.FetchItem{imap.FetchEnvelope, imap.FetchInternalDate, imap.FetchUid}
	messagesChan := make(chan *imap.Message, len(seqNums)) // Buffer for all found messages

	log.Printf("CheckForNewEmails: Fetching details for %d messages.", len(seqNums))
	if err := c.Fetch(seqSet, items, messagesChan); err != nil {
		// It's possible Fetch returns an error but still sends some messages.
		// Log the error and proceed with messages received if any.
		log.Printf("CheckForNewEmails: Error during Fetch (will process any messages received): %v", err)
		// Closing messagesChan is implicitly handled by the go-imap library when Fetch finishes or errors.
	}

	type EmailDetails struct {
		Subject string
		Date    time.Time
		UID     uint32 // For logging
	}
	var fetchedEmails []EmailDetails
	currentMaxDate := ic.lastSeenDate // Initialize with the current last seen date

	for msg := range messagesChan {
		log.Printf("CheckForNewEmails: Processing fetched message - UID: %d, Date: %s, Subject: '%s'",
			msg.Uid, msg.InternalDate.Format(time.RFC3339), msg.Envelope.Subject)

		// Only consider emails strictly after the lastSeenDate to avoid re-processing
		// emails that might have the exact same timestamp as lastSeenDate.
		if msg.InternalDate.After(ic.lastSeenDate) {
			fetchedEmails = append(fetchedEmails, EmailDetails{
				Subject: msg.Envelope.Subject,
				Date:    msg.InternalDate,
				UID:     msg.Uid,
			})
			log.Printf("CheckForNewEmails: Candidate new email - UID: %d, Date: %s", msg.Uid, msg.InternalDate.Format(time.RFC3339))
		} else {
			log.Printf("CheckForNewEmails: Skipping email (UID: %d, Date: %s) as it is not strictly after lastSeenDate (%s)",
				msg.Uid, msg.InternalDate.Format(time.RFC3339), ic.lastSeenDate.Format(time.RFC3339))
		}

		// Track the maximum date encountered in this batch, even if it's not "new" by the strict After check.
		// This ensures lastSeenDate progresses if new emails have same timestamp as old lastSeenDate.
		// However, the user said "if any email came at the same time shouldnt be a problem".
		// So, we should ONLY update lastSeenDate based on emails we actually consider "new".
		// The `currentMaxDate` will be updated based on successfully processed *new* emails.
	}

	if len(fetchedEmails) == 0 {
		log.Println("CheckForNewEmails: No emails found strictly after the lastSeenDate.")
		// It's possible that SINCE returned emails with the same timestamp as lastSeenDate.
		// We don't update lastSeenDate here as no *new* emails were processed.
		return newEmailSubjects, nil
	}

	// Sort the newly identified emails by date, most recent first
	sort.Slice(fetchedEmails, func(i, j int) bool {
		return fetchedEmails[i].Date.After(fetchedEmails[j].Date)
	})

	log.Printf("CheckForNewEmails: Found %d new email(s) after filtering and sorting:", len(fetchedEmails))
	for i, email := range fetchedEmails {
		newEmailSubjects = append(newEmailSubjects, email.Subject)
		log.Printf("CheckForNewEmails: New email #%d: UID %d, Date %s, Subject '%s'",
			i+1, email.UID, email.Date.Format(time.RFC3339), email.Subject)

		// Update currentMaxDate with the date of the newest email we are processing
		if email.Date.After(currentMaxDate) {
			currentMaxDate = email.Date
		}
	}

	// If we processed new emails, and the newest among them has a date later than our previous lastSeenDate, update it.
	if currentMaxDate.After(ic.lastSeenDate) {
		log.Printf("CheckForNewEmails: Updating lastSeenDate from %s to %s",
			ic.lastSeenDate.Format(time.RFC3339), currentMaxDate.Format(time.RFC3339))
		ic.lastSeenDate = currentMaxDate
		stateChanged = true
	}

	if stateChanged {
		ic.saveStateWithLogging("CheckForNewEmails - new emails processed, lastSeenDate updated")
	}

	log.Printf("CheckForNewEmails: Finished check. Returning %d new email subjects.", len(newEmailSubjects))
	return newEmailSubjects, nil
}

func (ic *ImapChecker) StartChecking(callback func([]string)) {
	go func() {
		log.Println("StartChecking: Performing initial email check...")
		// Initialize if needed on the first actual check
		if ic.lastSeenDate.IsZero() {
			log.Println("StartChecking: lastSeenDate is zero, performing initial tracking setup.")
			if err := ic.InitializeEmailTracking(); err != nil {
				log.Printf("StartChecking: Error during initial email tracking setup: %v", err)
				// Depending on severity, might want to stop or retry. For now, log and continue.
			}
		}

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

// ResetState clears the tracked last seen date for debugging
func (ic *ImapChecker) ResetState() {
	log.Println("ResetState: Clearing lastSeenDate.")
	ic.lastSeenDate = time.Time{} // Set to zero time

	// Save the reset state (zero date)
	ic.saveStateWithLogging("ResetState - cleared lastSeenDate")

	// Reinitialize tracking. This will fetch the latest email and set its date.
	log.Println("ResetState: Re-initializing email tracking to establish a new baseline date.")
	err := ic.InitializeEmailTracking()
	if err != nil {
		log.Printf("Warning: Failed to initialize email tracking after reset: %v", err)
	} else {
		log.Println("Email tracking re-initialized successfully after reset. New lastSeenDate should be set.")
	}
}
