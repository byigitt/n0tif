package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"sort"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
)

var (
	testImapServer = flag.String("server", "", "IMAP server address")
	testImapPort   = flag.Int("port", 993, "IMAP server port")
	testUsername   = flag.String("user", "", "Email username/address")
	testPassword   = flag.String("pass", "", "Email password")
	listAll        = flag.Bool("all", false, "List all emails (not just the most recent 20)")
	sortByDate     = flag.Bool("date", true, "Sort emails by date (newest first)")
)

func main() {
	flag.Parse()

	// Validate required inputs
	if *testImapServer == "" || *testUsername == "" || *testPassword == "" {
		fmt.Println("ERROR: Required flags: -server, -user, -pass")
		fmt.Println("Example: go run test_imap.go -server imap.example.com -port 993 -user user@example.com -pass mypassword")
		flag.PrintDefaults()
		os.Exit(1)
	}

	// Connect to server
	fmt.Printf("Connecting to %s:%d...\n", *testImapServer, *testImapPort)
	serverAddr := fmt.Sprintf("%s:%d", *testImapServer, *testImapPort)

	c, err := client.DialTLS(serverAddr, nil)
	if err != nil {
		log.Fatalf("Failed to connect: %v", err)
	}
	defer c.Logout()
	fmt.Println("Connected successfully!")

	// Login
	fmt.Printf("Logging in as %s...\n", *testUsername)
	if err := c.Login(*testUsername, *testPassword); err != nil {
		log.Fatalf("Failed to login: %v", err)
	}
	fmt.Println("Logged in successfully!")

	// Select INBOX
	mbox, err := c.Select("INBOX", true)
	if err != nil {
		log.Fatalf("Failed to select INBOX: %v", err)
	}
	fmt.Printf("INBOX selected. Total messages: %d\n", mbox.Messages)

	// Determine how many messages to fetch
	fetchCount := uint32(20) // Default to 20 messages
	if *listAll {
		fetchCount = mbox.Messages
	}

	if fetchCount == 0 {
		fmt.Println("No messages in INBOX.")
		return
	}

	if fetchCount > mbox.Messages {
		fetchCount = mbox.Messages
	}

	from := uint32(1)
	if mbox.Messages > fetchCount {
		from = mbox.Messages - fetchCount + 1
	}

	// Create a sequence set for the desired messages
	seqSet := new(imap.SeqSet)
	seqSet.AddRange(from, mbox.Messages)

	// Request message attributes
	items := []imap.FetchItem{imap.FetchUid, imap.FetchEnvelope, imap.FetchInternalDate, imap.FetchFlags}
	messages := make(chan *imap.Message, fetchCount)

	// Fetch messages
	fmt.Printf("Fetching %d messages...\n", fetchCount)
	done := make(chan error, 1)
	go func() {
		done <- c.Fetch(seqSet, items, messages)
	}()

	// Collect messages
	type EmailInfo struct {
		UID      uint32
		Date     time.Time
		Subject  string
		From     string
		SeqNum   uint32
		Flags    []string
		IsRecent bool
	}

	var emails []EmailInfo
	for msg := range messages {
		// Extract From name/address
		fromStr := "Unknown"
		if len(msg.Envelope.From) > 0 {
			addr := msg.Envelope.From[0]
			if addr.PersonalName != "" {
				fromStr = addr.PersonalName
			} else if addr.MailboxName != "" && addr.HostName != "" {
				fromStr = fmt.Sprintf("%s@%s", addr.MailboxName, addr.HostName)
			}
		}

		// Check if recent flag is set
		isRecent := false
		for _, flag := range msg.Flags {
			if flag == "\\Recent" {
				isRecent = true
				break
			}
		}

		emails = append(emails, EmailInfo{
			UID:      msg.Uid,
			Date:     msg.InternalDate,
			Subject:  msg.Envelope.Subject,
			From:     fromStr,
			SeqNum:   msg.SeqNum,
			Flags:    msg.Flags,
			IsRecent: isRecent,
		})
	}

	if err := <-done; err != nil {
		log.Fatalf("Failed to fetch messages: %v", err)
	}

	// Sort messages by date (newest first) if requested
	if *sortByDate {
		sort.Slice(emails, func(i, j int) bool {
			return emails[i].Date.After(emails[j].Date)
		})
	} else {
		// Otherwise, sort by UID (ascending)
		sort.Slice(emails, func(i, j int) bool {
			return emails[i].UID < emails[j].UID
		})
	}

	// Print table header
	fmt.Println("\n+-------+--------+----------------------+-----------------------------+----------------------------------------------------+----------+")
	fmt.Println("| SeqNum|   UID  |         Date         |            From             |                     Subject                        |  Recent  |")
	fmt.Println("+-------+--------+----------------------+-----------------------------+----------------------------------------------------+----------+")

	// Print message details
	for _, email := range emails {
		subject := email.Subject
		if len(subject) > 50 {
			subject = subject[:47] + "..."
		}

		from := email.From
		if len(from) > 27 {
			from = from[:24] + "..."
		}

		recentMark := " "
		if email.IsRecent {
			recentMark = "*"
		}

		fmt.Printf("| %5d | %6d | %s | %-27s | %-50s | %-8s |\n",
			email.SeqNum,
			email.UID,
			email.Date.Format("2006-01-02 15:04:05"),
			from,
			subject,
			recentMark)
	}
	fmt.Println("+-------+--------+----------------------+-----------------------------+----------------------------------------------------+----------+")

	// Print summary
	if *sortByDate {
		fmt.Printf("\nShowing %d emails sorted by date (newest first)\n", len(emails))
	} else {
		fmt.Printf("\nShowing %d emails sorted by UID (lowest first)\n", len(emails))
	}

	// Show highest UID
	highestUID := uint32(0)
	for _, email := range emails {
		if email.UID > highestUID {
			highestUID = email.UID
		}
	}
	fmt.Printf("Highest UID found: %d\n", highestUID)

	// Logout
	fmt.Println("\nLogging out...")
	c.Logout()
}
