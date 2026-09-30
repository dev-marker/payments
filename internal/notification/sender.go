package notification

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"net/textproto"
	"time"
)

// A Sender delivers messages. In production it's an email provider's API;
// in development, Mailpit, which catches everything we send.
type Sender interface {
	Send(ctx context.Context, m Message) error
}

// SMTP sends messages through an SMTP server.
type SMTP struct {
	Addr string // host:port
	From string // an address, like no-reply@payments.example
}

// Send sends m. A 5xx reply, like "no such mailbox", is permanent. Anything
// else, like a server that's down, might work later.
func (s SMTP) Send(ctx context.Context, m Message) error {
	err := s.send(ctx, m)
	var reply *textproto.Error
	if errors.As(err, &reply) && reply.Code >= 500 {
		return Permanent(err)
	}
	return err
}

func (s SMTP) send(ctx context.Context, m Message) error {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", s.Addr)
	if err != nil {
		return fmt.Errorf("smtp: %w", err)
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline) // net/smtp knows nothing about contexts
	}
	host, _, _ := net.SplitHostPort(s.Addr)
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		return fmt.Errorf("smtp: %w", err)
	}
	if err := c.Mail(s.From); err != nil {
		return err
	}
	if err := c.Rcpt(m.To); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(format(s.From, m)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// format writes m as an email. Its Message-ID is the event's ID: a mail
// system that sees the same ID twice can drop the copy.
func format(from string, m Message) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "From: Payments <%s>\r\nTo: %s\r\nSubject: %s\r\n", from, m.To, m.Subject)
	fmt.Fprintf(&b, "Message-ID: <%s@payments.example>\r\n", m.EventID)
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	fmt.Fprintf(&b, "Content-Type: text/plain; charset=utf-8\r\n\r\n%s\r\n", m.Body)
	return b.Bytes()
}
