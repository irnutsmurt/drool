package notify

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"
)

// sendEmail delivers over SMTP. The whole exchange (DNS, connect, TLS,
// every command) shares ctx's deadline. STARTTLS is required when chosen:
// DROOL never falls back to plaintext, and never sends a password
// unencrypted. Certificates are verified against the system roots.
func (s *Sender) sendEmail(ctx context.Context, c Config, sec Secrets, m Message, deliveryID string) Result {
	addr := net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	conn, err := (&net.Dialer{Timeout: dialTimeout}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return classifyNetErr(err)
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}
	stop := context.AfterFunc(ctx, func() { conn.SetDeadline(time.Now()) })
	defer stop()
	stage := "connect"
	smtpFail := func(err error) Result {
		r := classifySMTP(err, stage)
		r.Ambiguous = stage == "data-end" && (r.Category == CategoryTimeout || r.Category == CategoryNetwork)
		return r
	}
	if c.Security == SecurityTLS {
		tc := tls.Client(conn, s.tlsConfig(c.Host))
		hctx, cancel := context.WithTimeout(ctx, handshakeTimeout)
		err := tc.HandshakeContext(hctx)
		cancel()
		if err != nil {
			return classifyNetErr(err)
		}
		conn = tc
	}
	cl, err := smtp.NewClient(conn, c.Host)
	if err != nil {
		return smtpFail(err)
	}
	defer cl.Close()
	helo := s.HeloName
	if helo == "" {
		helo = hostName()
	}
	stage = "hello"
	if err := cl.Hello(helo); err != nil {
		return smtpFail(err)
	}
	if c.Security == SecurityStartTLS {
		if ok, _ := cl.Extension("STARTTLS"); !ok {
			return fail(OutcomePermanent, CategoryConfig, "the server doesn't offer STARTTLS; DROOL won't send unencrypted (choose TLS or check the port)")
		}
		stage = "starttls"
		if err := cl.StartTLS(s.tlsConfig(c.Host)); err != nil {
			if r := classifyNetErr(err); r.Category == CategoryTLS {
				return r
			}
			return smtpFail(err)
		}
	}
	if c.Username != "" {
		if ok, _ := cl.Extension("AUTH"); !ok {
			return fail(OutcomePermanent, CategoryConfig, "the server doesn't offer authentication (AUTH); remove the username or use the submission port")
		}
		stage = "auth"
		if err := cl.Auth(smtp.PlainAuth("", c.Username, sec.Password, c.Host)); err != nil {
			return smtpFail(err)
		}
	}
	stage = "mail"
	from := c.From
	if a, err := parseAddr(from); err == nil {
		from = a
	}
	if err := cl.Mail(from); err != nil {
		return smtpFail(err)
	}
	stage = "rcpt"
	for _, to := range c.To {
		if err := cl.Rcpt(to); err != nil {
			return smtpFail(err)
		}
	}
	stage = "data"
	w, err := cl.Data()
	if err != nil {
		return smtpFail(err)
	}
	if _, err := w.Write(buildEmail(c, m, deliveryID, helo)); err != nil {
		return smtpFail(err)
	}
	stage = "data-end"
	if err := w.Close(); err != nil {
		return smtpFail(err)
	}
	stage = "quit"
	cl.Quit() // the message is accepted; a failing QUIT changes nothing
	return Result{Outcome: OutcomeDelivered, Category: CategoryOK, Message: "accepted by " + c.Host}
}

func parseAddr(s string) (string, error) {
	i, j := strings.LastIndex(s, "<"), strings.LastIndex(s, ">")
	if i >= 0 && j > i {
		return s[i+1 : j], nil
	}
	return strings.TrimSpace(s), nil
}

func classifySMTP(err error, stage string) Result {
	var te *textproto.Error
	if errors.As(err, &te) {
		msg := fmt.Sprintf("SMTP %d %s", te.Code, oneLine(clip(te.Msg, 200)))
		switch {
		case stage == "auth" && te.Code == 454:
			return Result{Outcome: OutcomeTransient, Category: CategoryAuth, StatusCode: te.Code, Message: "temporary authentication failure: " + msg}
		case stage == "auth" || te.Code == 530 || te.Code == 534 || te.Code == 535 || te.Code == 538:
			return Result{Outcome: OutcomePermanent, Category: CategoryAuth, StatusCode: te.Code, Message: "authentication failed: " + msg}
		case te.Code >= 400 && te.Code < 500:
			return Result{Outcome: OutcomeTransient, Category: CategoryServer, StatusCode: te.Code, Message: "the server deferred the message: " + msg}
		default:
			return Result{Outcome: OutcomePermanent, Category: CategoryRejected, StatusCode: te.Code, Message: "the server rejected the message: " + msg}
		}
	}
	if strings.Contains(err.Error(), "unencrypted connection") {
		return fail(OutcomePermanent, CategoryConfig, "refusing to send the password over an unencrypted connection")
	}
	r := classifyNetErr(err)
	if r.Category == CategoryNetwork && !strings.HasPrefix(r.Message, "connection") {
		r.Message = "connection failed during " + stage + ": " + r.Message
	}
	return r
}

// headerSafe strips CR/LF so nothing in a host or service name can inject
// a header.
func headerSafe(s string) string { return oneLine(s) }

func buildEmail(c Config, m Message, deliveryID, helo string) []byte {
	var b bytes.Buffer
	h := func(k, v string) { b.WriteString(k + ": " + headerSafe(v) + "\r\n") }
	h("From", c.From)
	h("To", strings.Join(c.To, ", "))
	subject := Headline(m)
	if m.Kind != MsgTest {
		subject += " — " + m.Title
	}
	h("Subject", mime.QEncoding.Encode("utf-8", clip(subject, 200)))
	h("Date", time.Now().Format(time.RFC1123Z))
	id := deliveryID
	if id == "" {
		r := make([]byte, 8)
		rand.Read(r)
		id = "test-" + hex.EncodeToString(r)
	}
	h("Message-ID", "<"+id+"@"+headerSafe(helo)+">")
	if m.EventID != "" {
		h("X-DROOL-Event", m.EventID)
	}
	h("Auto-Submitted", "auto-generated")
	h("MIME-Version", "1.0")
	h("Content-Type", "text/plain; charset=utf-8")
	h("Content-Transfer-Encoding", "quoted-printable")
	b.WriteString("\r\n")
	qp := quotedprintable.NewWriter(&b)
	qp.Write([]byte(strings.ReplaceAll(Text(m), "\n", "\r\n")))
	qp.Close()
	return b.Bytes()
}
