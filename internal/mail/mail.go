package mail

import "log/slog"

// Mailer abstracts outbound email so SMTP/SES can be wired in later without
// touching handlers.
type Mailer interface {
	SendPasswordReset(to, token string) error
}

// LogMailer is the development implementation: it logs that a reset was
// issued. The token itself is never logged — in dev, read it from the DB.
type LogMailer struct{ log *slog.Logger }

func NewLogMailer(log *slog.Logger) *LogMailer { return &LogMailer{log: log} }

func (m *LogMailer) SendPasswordReset(to, token string) error {
	m.log.Info("password reset issued (email delivery not configured)", "to", redact(to))
	return nil
}

func redact(email string) string {
	if len(email) < 3 {
		return "***"
	}
	return email[:2] + "***"
}
