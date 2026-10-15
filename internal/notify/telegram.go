package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"drool/internal/version"
)

// Telegram (final After-M17 batch): a bot posts to a chat through the Bot
// API's sendMessage. The bot token is a secret, and Telegram puts it in the
// request URL (https://api.telegram.org/bot<TOKEN>/sendMessage), so that URL
// is built only here, for the request, and never leaves this file: not in
// the configuration (no URLDisplay), not in results or errors (transport
// errors carry the URL; they are reduced to their cause, and every result is
// scrubbed of every form of the token), and nothing here logs.

const telegramAPI = "https://api.telegram.org"

var (
	// 123456789:AA… : the bot's id, a colon, and the secret part.
	telegramTokenRe = regexp.MustCompile(`^[0-9]{1,20}:[A-Za-z0-9_-]{30,100}$`)
	telegramChatRe  = regexp.MustCompile(`^(-?[0-9]{1,20}|@[A-Za-z][A-Za-z0-9_]{3,31})$`)
	// A mention: "@" before a name. A word joiner after the "@" keeps host
	// and service names from notifying people in a group, as on Discord.
	telegramMentionRe = regexp.MustCompile(`@([A-Za-z0-9_])`)
)

// tokenForms are the ways a bot token can appear in text: as is, escaped,
// in the URL path ("bot" + token), and its secret part alone.
func tokenForms(tok string) []string {
	if tok == "" {
		return nil
	}
	out := []string{tok, "bot" + tok, url.QueryEscape(tok), url.PathEscape(tok), strings.ReplaceAll(tok, ":", "%3A")}
	if _, secret, ok := strings.Cut(tok, ":"); ok && len(secret) >= 16 {
		out = append(out, secret)
	}
	return out
}

func normalizeTelegram(c *Config, s *Secrets) error {
	s.BotToken = strings.TrimSpace(s.BotToken)
	if s.BotToken == "" {
		return cfgErr("enter the bot token from @BotFather")
	}
	if !telegramTokenRe.MatchString(s.BotToken) {
		// Never repeat what was pasted: it may be a real token with a typo.
		return cfgErr("that doesn't look like a bot token: @BotFather gives one like 123456789:AAH… (digits, a colon, then letters, digits, - and _)")
	}
	c.ChatID = strings.TrimSpace(c.ChatID)
	if c.ChatID == "" {
		return cfgErr("enter the chat ID: a number such as -1001234567890 for a group or channel, or @channelname for a public channel")
	}
	if !telegramChatRe.MatchString(c.ChatID) {
		return cfgErr("the chat ID is a number such as -1001234567890 (groups and channels), 123456789 (a person), or @channelname (a public channel)")
	}
	if c.ThreadID < 0 || c.ThreadID > 1<<53 {
		return cfgErr("the topic ID is a positive number, or empty for the chat itself")
	}
	return nil
}

// telegramBody is sendMessage's request: HTML with only a bold headline,
// every other character escaped, no link previews.
func telegramBody(c Config, m Message) map[string]any {
	esc := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	quiet := func(s string) string { return telegramMentionRe.ReplaceAllString(esc.Replace(s), "@\u2060$1") }
	// Clipped before escaping, so no entity is ever cut in half; Telegram
	// counts the text after entities, at most 4096 characters.
	text := "<b>" + quiet(clip(oneLine(Headline(m)), 250)) + "</b>\n" + quiet(clip(strings.Join(Lines(m), "\n"), 3500))
	b := map[string]any{"text": text, "parse_mode": "HTML", "link_preview_options": map[string]any{"is_disabled": true}}
	if n, err := strconv.ParseInt(c.ChatID, 10, 64); err == nil {
		b["chat_id"] = n
	} else {
		b["chat_id"] = c.ChatID
	}
	if c.ThreadID > 0 {
		b["message_thread_id"] = c.ThreadID
	}
	return b
}

func (s *Sender) sendTelegram(ctx context.Context, c Config, sec Secrets, m Message) Result {
	if err := normalizeTelegram(&c, &sec); err != nil {
		return fail(OutcomePermanent, CategoryConfig, err.Error())
	}
	base := telegramAPI
	if s.TelegramAPI != "" {
		base = strings.TrimRight(s.TelegramAPI, "/")
	}
	body, _ := json.Marshal(telegramBody(c, m))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/bot"+sec.BotToken+"/sendMessage", bytes.NewReader(body))
	if err != nil {
		return fail(OutcomePermanent, CategoryConfig, "the Telegram request could not be built") // never err: it quotes the URL
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "DROOL/"+version.Version)
	var wrote bool
	req = req.WithContext(httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		WroteRequest: func(i httptrace.WroteRequestInfo) { wrote = i.Err == nil },
	}))
	// Never follow a redirect: the token is in the path.
	client := &http.Client{Transport: s.httpTransport(), CheckRedirect: func(*http.Request, []*http.Request) error { return errRedirect }}
	resp, err := client.Do(req)
	if errors.Is(err, errRedirect) {
		return fail(OutcomePermanent, CategoryConfig, "Telegram answered with a redirect, which DROOL doesn't follow (the request carries the bot token)")
	}
	if err != nil {
		r := classifyNetErr(err) // drops the url.Error and its URL
		r.Ambiguous = wrote && r.Category == CategoryTimeout
		return r
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	io.Copy(io.Discard, io.LimitReader(resp.Body, MaxResponseBody))
	return classifyTelegram(resp, raw)
}

// classifyTelegram reads the Bot API's answer: {"ok": false, "error_code":
// 400, "description": "Bad Request: chat not found", "parameters": {...}}.
func classifyTelegram(resp *http.Response, raw []byte) Result {
	var tr struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
		Parameters  struct {
			RetryAfter      int   `json:"retry_after"`
			MigrateToChatID int64 `json:"migrate_to_chat_id"`
		} `json:"parameters"`
	}
	json.Unmarshal(raw, &tr)
	code := resp.StatusCode
	r := Result{StatusCode: code}
	desc := oneLine(clip(strings.TrimSpace(tr.Description), 200))
	if desc == "" {
		desc = http.StatusText(code)
	}
	low := strings.ToLower(desc)
	switch {
	case code == 200 && tr.OK:
		r.Outcome, r.Category, r.Message = OutcomeDelivered, CategoryOK, "delivered (Telegram)"
	case code == 429:
		r.Outcome, r.Category = OutcomeTransient, CategoryRateLimited
		r.RetryAfter = min(time.Duration(tr.Parameters.RetryAfter)*time.Second, time.Hour)
		if r.RetryAfter <= 0 {
			r.RetryAfter = retryAfter(resp.Header.Get("Retry-After"))
		}
		r.Message = "rate limited by Telegram (HTTP 429)"
		if r.RetryAfter > 0 {
			r.Message += fmt.Sprintf("; retrying after %s", Duration(r.RetryAfter))
		}
	case code == 401 || code == 404:
		r.Outcome, r.Category = OutcomePermanent, CategoryAuth
		r.Message = fmt.Sprintf("Telegram refused the bot token (HTTP %d: %s): copy it again from @BotFather", code, desc)
	case code == 400 && tr.Parameters.MigrateToChatID != 0:
		r.Outcome, r.Category = OutcomePermanent, CategoryConfig
		r.Message = fmt.Sprintf("the group was upgraded to a supergroup: change the chat ID to %d", tr.Parameters.MigrateToChatID)
	case code == 403:
		r.Outcome, r.Category = OutcomePermanent, CategoryRejected
		r.Message = fmt.Sprintf("the bot can't post in this chat (HTTP 403: %s): add it to the group or channel (a channel needs it as an administrator), or send the bot a message first", desc)
	case code == 400 && strings.Contains(low, "chat not found"):
		r.Outcome, r.Category = OutcomePermanent, CategoryRejected
		r.Message = "Telegram doesn't know this chat (HTTP 400: " + desc + "): check the chat ID, and that the bot is in the chat"
	case code == 400 && strings.Contains(low, "thread not found"):
		r.Outcome, r.Category = OutcomePermanent, CategoryRejected
		r.Message = "Telegram doesn't know this topic (HTTP 400: " + desc + "): check the topic ID, or leave it empty"
	case code == 408:
		r.Outcome, r.Category, r.Message = OutcomeTransient, CategoryTimeout, "Telegram timed out (HTTP 408)"
	case code >= 500:
		r.Outcome, r.Category, r.Message = OutcomeTransient, CategoryServer, fmt.Sprintf("Telegram failed (HTTP %d: %s)", code, desc)
		r.RetryAfter = retryAfter(resp.Header.Get("Retry-After"))
	case code >= 300 && code < 400:
		r.Outcome, r.Category, r.Message = OutcomePermanent, CategoryConfig, fmt.Sprintf("unexpected redirect from Telegram (HTTP %d)", code)
	default:
		r.Outcome, r.Category, r.Message = OutcomePermanent, CategoryRejected, fmt.Sprintf("Telegram rejected the message (HTTP %d: %s)", code, desc)
	}
	return r
}
