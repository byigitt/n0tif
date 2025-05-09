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

	numToFetch := uint32(5)
	if mbox.Messages < numToFetch {
		numToFetch = mbox.Messages
	}
	from := mbox.Messages - numToFetch + 1

	seqSetForInit := new(imap.SeqSet) // Renamed to avoid conflict if we re-introduce for fallback
	seqSetForInit.AddRange(from, mbox.Messages)

	items := []imap.FetchItem{imap.FetchUid, imap.FetchEnvelope} // Corrected
	messagesChan := make(chan *imap.Message, numToFetch)

	log.Printf("InitializeEmailTracking: Fetching messages %d-%d to establish baseline.", from, mbox.Messages)
	done := make(chan error, 1)
	go func() {
		done <- c.Fetch(seqSetForInit, items, messagesChan)
	}()

	baselineUIDsAdded := 0
	for msg := range messagesChan {
		ic.emailState.AddUID(mailboxName, msg.Uid)
		ic.notifiedUIDs[msg.Uid] = true
		baselineUIDsAdded++
		log.Printf("InitializeEmailTracking: Added UID %d (Subject: '%s') to baseline.", msg.Uid, msg.Envelope.Subject)
	}

	if err := <-done; err != nil {
		return fmt.Errorf("InitializeEmailTracking fetch messages: %w", err)
	}

	if baselineUIDsAdded > 0 {
		ic.saveStateWithLogging(fmt.Sprintf("InitializeEmailTracking - %d baseline UIDs added", baselineUIDsAdded))
	} else {
		log.Println("InitializeEmailTracking: No messages fetched for baseline.")
	}

	log.Printf("InitializeEmailTracking: Baseline established with %d UIDs.", baselineUIDsAdded)
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

	highestKnownUID := ic.emailState.GetHighestUID(mailboxName)
	log.Printf("CheckForNewEmails: Highest known UID from state for %s: %d", mailboxName, highestKnownUID)

	var criteria *imap.SearchCriteria
	if highestKnownUID == 0 && mbox.Messages > 0 {
		log.Printf("CheckForNewEmails: No highest UID known, but %d messages exist. Will fetch by SeqNum and filter by notifiedUIDs.", mbox.Messages)
		criteria = imap.NewSearchCriteria()
		criteria.SeqNum = new(imap.SeqSet)
		criteria.SeqNum.AddRange(1, mbox.Messages)
	} else if highestKnownUID > 0 {
		log.Printf("CheckForNewEmails: Searching for UIDs greater than %d", highestKnownUID)
		criteria = imap.NewSearchCriteria()
		criteria.Uid = new(imap.SeqSet)
		criteria.Uid.AddRange(highestKnownUID+1, 0)
	} else {
		log.Println("CheckForNewEmails: No messages to check.")
		return newEmailSubjects, nil
	}

	uidsOrSeqNumsToFetch, err := c.Search(criteria)
	if err != nil {
		return nil, fmt.Errorf("CheckForNewEmails search: %w", err)
	}

	if len(uidsOrSeqNumsToFetch) == 0 {
		log.Println("CheckForNewEmails: No messages found matching search criteria.")
		return newEmailSubjects, nil
	}
	log.Printf("CheckForNewEmails: Search returned %d IDs: %v", len(uidsOrSeqNumsToFetch), uidsOrSeqNumsToFetch)

	itemsToFetch := []imap.FetchItem{imap.FetchEnvelope, imap.FetchUid} // Corrected
	messagesChan := make(chan *imap.Message, len(uidsOrSeqNumsToFetch))
	done := make(chan error, 1)

	fetchByUID := (highestKnownUID > 0)

	fetchSet := new(imap.SeqSet) // This set will contain UIDs if fetchByUID is true, or SeqNums otherwise
	for _, id := range uidsOrSeqNumsToFetch {
		fetchSet.AddNum(id)
	}

	go func() {
		if fetchByUID {
			log.Printf("CheckForNewEmails: UidFetching %d UIDs: %v", len(uidsOrSeqNumsToFetch), uidsOrSeqNumsToFetch)
			done <- c.UidFetch(fetchSet, itemsToFetch, messagesChan)
		} else {
			log.Printf("CheckForNewEmails: Fetching %d messages by SeqNum: %v", len(uidsOrSeqNumsToFetch), uidsOrSeqNumsToFetch)
			done <- c.Fetch(fetchSet, itemsToFetch, messagesChan)
		}
	}()

	for msg := range messagesChan {
		if !ic.notifiedUIDs[msg.Uid] {
			log.Printf("CheckForNewEmails: New email UID %d (Subject: '%s') - adding to notifications.", msg.Uid, msg.Envelope.Subject)
			newEmailSubjects = append(newEmailSubjects, msg.Envelope.Subject)
			ic.notifiedUIDs[msg.Uid] = true
			ic.emailState.AddUID(mailboxName, msg.Uid)
			stateChanged = true
		} else {
			log.Printf("CheckForNewEmails: Email UID %d (Subject: '%s') already notified or part of initial baseline.", msg.Uid, msg.Envelope.Subject)
			foundInState := false
			for _, stateUid := range ic.emailState.GetLastUIDs(mailboxName) {
				if stateUid == msg.Uid {
					foundInState = true
					break
				}
			}
			if !foundInState {
				log.Printf("CheckForNewEmails: UID %d was in notifiedUIDs but not in emailState storage, adding to storage now.", msg.Uid)
				ic.emailState.AddUID(mailboxName, msg.Uid)
				stateChanged = true
			}
		}
	}

	if err := <-done; err != nil {
		return nil, fmt.Errorf("CheckForNewEmails fetch messages: %w", err)
	}

	if stateChanged {
		ic.saveStateWithLogging("CheckForNewEmails - new UIDs processed")
	}

	log.Printf("CheckForNewEmails: Finished check. Returning %d new email subjects.", len(newEmailSubjects))
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
