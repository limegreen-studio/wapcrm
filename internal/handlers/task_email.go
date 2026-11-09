package handlers

import (
	"crypto/tls"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"

	"github.com/limegreen-studio/wapcrm/internal/models"
)

// emailTimeout bounds one SMTP conversation.
const emailTimeout = 15 * time.Second

// emailTaskDue emails the assignee in the background. It does nothing when
// SMTP is not configured or the task has no assignee.
func (a *App) emailTaskDue(task *models.Task, contactName, contactPhone string) {
	if a.Config == nil || a.Config.Email.Host == "" || task.AssigneeID == nil {
		return
	}
	var user models.User
	if err := a.DB.Select("id", "email").Where("id = ?", *task.AssigneeID).First(&user).Error; err != nil || user.Email == "" {
		return
	}
	who := contactName
	if who == "" {
		who = contactPhone
	}
	subject := "Follow-up due: " + task.Title
	body := fmt.Sprintf("%s\n\nContact: %s\n", task.Title, who)
	if task.Notes != "" {
		body += "\n" + task.Notes + "\n"
	}
	to := user.Email
	go func() {
		if err := a.sendEmail(to, subject, body); err != nil {
			a.Log.Error("Failed to send follow-up email", "error", err, "task_id", task.ID)
		}
	}()
}

func (a *App) sendEmail(to, subject, body string) error {
	cfg := a.Config.Email
	from, err := mail.ParseAddress(cfg.From)
	if err != nil {
		return fmt.Errorf("invalid email.from: %w", err)
	}
	port := cfg.Port
	if port == 0 {
		port = 587
	}
	addr := net.JoinHostPort(cfg.Host, fmt.Sprint(port))
	conn, err := net.DialTimeout("tcp", addr, emailTimeout)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(emailTimeout))

	client, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		return err
	}
	defer client.Close()
	if ok, _ := client.Extension("STARTTLS"); ok {
		if err := client.StartTLS(&tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12}); err != nil {
			return err
		}
	}
	if cfg.Username != "" {
		if err := client.Auth(smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)); err != nil {
			return err
		}
	}
	if err := client.Mail(from.Address); err != nil {
		return err
	}
	if err := client.Rcpt(to); err != nil {
		return err
	}
	w, err := client.Data()
	if err != nil {
		return err
	}
	// Task titles are user text: encoding removes any header line breaks
	msg := "From: " + from.String() + "\r\nTo: " + to +
		"\r\nSubject: " + mime.QEncoding.Encode("utf-8", subject) +
		"\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" +
		strings.ReplaceAll(body, "\n", "\r\n")
	if _, err := w.Write([]byte(msg)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return client.Quit()
}
