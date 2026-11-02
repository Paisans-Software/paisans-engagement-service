package mail

import (
	"bytes"
	"embed"
	"fmt"
	htmltemplate "html/template"
	"strings"
	texttemplate "text/template"
)

//go:embed templates/welcome.html templates/welcome.txt
var welcomeFiles embed.FS

var (
	welcomeHTML = htmltemplate.Must(htmltemplate.ParseFS(welcomeFiles, "templates/welcome.html"))
	welcomeText = texttemplate.Must(texttemplate.ParseFS(welcomeFiles, "templates/welcome.txt"))
)

// WelcomeLine is one newcomer in the welcome email: their name and their
// welcome thread.
type WelcomeLine struct{ Name, URL string }

// WelcomeSubject is the welcome email's subject for n newcomers.
func WelcomeSubject(n int, meta string) string {
	if n == 1 {
		return "Say hello to 1 new person on " + meta
	}
	return fmt.Sprintf("Say hello to %d new people on %s", n, meta)
}

// RenderWelcome builds the welcome email for one member. magazineURL is the
// welcome magazine, which the button links to.
func RenderWelcome(lines []WelcomeLine, n Names, magazineURL, unsubscribeURL string) (Message, error) {
	subject := WelcomeSubject(len(lines), n.Meta)
	v := struct {
		Heading, Meta, MagazineURL, Unsubscribe string
		Lines                                   []WelcomeLine
	}{subject, n.Meta, strings.TrimRight(magazineURL, "/"), unsubscribeURL, lines}
	var html, text bytes.Buffer
	if err := welcomeHTML.Execute(&html, v); err != nil {
		return Message{}, fmt.Errorf("render welcome html: %w", err)
	}
	if err := welcomeText.Execute(&text, v); err != nil {
		return Message{}, fmt.Errorf("render welcome text: %w", err)
	}
	return Message{Subject: subject, Text: text.String(), HTML: html.String(), Unsubscribe: unsubscribeURL}, nil
}
