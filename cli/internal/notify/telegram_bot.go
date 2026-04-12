package notify

import (
	"context"
	"html"
	"log/slog"
	"strings"
	"time"
)

// CommandRunner executes a named command and returns its combined output.
// Implementations must not shell out to a real subprocess in tests; use stubs.
type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// BotDispatcher dispatches inbound Telegram messages to allowed CLI commands
// and sends the output back to the chat.
type BotDispatcher struct {
	tg               *Telegram
	allowedCommands  map[string]bool
	authorizedChatID int64
	runner           CommandRunner
	timeout          time.Duration
}

// NewBotDispatcher creates a BotDispatcher. allowedCommands is a list of full
// command strings (e.g. "xylem status", "gh pr list") that the bot may run.
// The dispatcher only responds to messages from authorizedChatID.
func NewBotDispatcher(tg *Telegram, authorizedChatID int64, allowedCommands []string, runner CommandRunner) *BotDispatcher {
	allowed := make(map[string]bool, len(allowedCommands))
	for _, cmd := range allowedCommands {
		allowed[strings.TrimSpace(cmd)] = true
	}
	return &BotDispatcher{
		tg:               tg,
		allowedCommands:  allowed,
		authorizedChatID: authorizedChatID,
		runner:           runner,
		timeout:          30 * time.Second,
	}
}

// Dispatch handles an inbound message. It is intended to be passed to
// Telegram.StartPolling as the handler function.
func (b *BotDispatcher) Dispatch(ctx context.Context, chatID int64, text string) {
	if chatID != b.authorizedChatID {
		slog.Debug("telegram bot: ignoring message from unauthorized chat", "chat_id", chatID)
		return
	}

	parts := parseCommand(text)
	if len(parts) == 0 {
		return
	}

	cmdKey := strings.Join(parts, " ")
	if !b.allowedCommands[cmdKey] {
		reply := "Command not allowed: " + html.EscapeString(cmdKey)
		if err := b.tg.Send(ctx, reply); err != nil {
			slog.Warn("telegram bot: failed to send disallowed command reply", "error", err)
		}
		return
	}

	cmdCtx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()

	out, err := b.runner.Run(cmdCtx, parts[0], parts[1:]...)
	if err != nil {
		reply := "<b>Error:</b> " + html.EscapeString(err.Error())
		if sendErr := b.tg.Send(ctx, reply); sendErr != nil {
			slog.Warn("telegram bot: failed to send error reply", "error", sendErr)
		}
		return
	}

	reply := string(out)
	if reply == "" {
		reply = "(no output)"
	}
	maxLen := telegramMaxMsgLen - 50
	if len(reply) > maxLen {
		reply = reply[:maxLen-3] + "..."
	}
	reply = "<pre>" + html.EscapeString(reply) + "</pre>"

	if err := b.tg.Send(ctx, reply); err != nil {
		slog.Warn("telegram bot: failed to send command output", "error", err)
	}
}

// parseCommand splits a Telegram message into command tokens by whitespace.
// Returns nil for empty or whitespace-only input.
func parseCommand(text string) []string {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return nil
	}
	return fields
}
