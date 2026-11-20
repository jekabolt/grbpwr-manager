package platformmail

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/mail"
)

func TestRenderLetters(t *testing.T) {
	tpl, err := parseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	until := time.Date(2026, 12, 20, 23, 30, 0, 0, time.UTC)
	deleted := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	cases := []struct {
		kind    string
		p       Payload
		subject string
		want    []string
	}{
		{KindVerify, Payload{Code: "482913", Link: "https://beta.lekalo.studio/confirm#token=abc", Slug: "atelier"},
			"482 913 · confirm email", []string{"482 913", "confirm#token=abc", "atelier.beta.lekalo.studio"}},
		{KindReset, Payload{Code: "731054", Link: "https://beta.lekalo.studio/reset#token=def"},
			"731 054 · reset password", []string{"731 054", "reset#token=def"}},
		{KindInvite, Payload{TeamName: "Atelier & Co", From: "anna", Link: "https://beta.lekalo.studio/join#token=ghi"},
			"join Atelier & Co", []string{"join#token=ghi", "anna"}},
		{KindTeamRequest, Payload{TeamName: "Atelier Anna", Slug: "atelier-anna", Email: "anna@example.com", Link: "https://console.beta.lekalo.studio/"},
			"new team · Atelier Anna", []string{"Atelier Anna", "atelier-anna.beta.lekalo.studio", "anna@example.com", "console.beta.lekalo.studio"}},
		{KindTeamApproved, Payload{TeamName: "Atelier Anna", Slug: "atelier-anna", Link: "https://atelier-anna.beta.lekalo.studio/"},
			"Atelier Anna · approved", []string{"open workspace", "https://atelier-anna.beta.lekalo.studio/"}},
		{KindRestore, Payload{Code: "120934", Link: "https://beta.lekalo.studio/restore#token=jkl", Until: &until, Deleted: &deleted},
			"120 934 · restore account", []string{"120 934", "restore#token=jkl", "restore until dec 20", "deleted nov 20"}},
		{KindAccountDeleted, Payload{Link: "https://beta.lekalo.studio/restore", Until: &until},
			"account deleted", []string{"restore until dec 20", "https://beta.lekalo.studio/restore"}},
	}
	for _, c := range cases {
		m, err := tpl.render(c.kind, c.p, "beta.lekalo.studio")
		if err != nil {
			t.Fatalf("%s: %v", c.kind, err)
		}
		if m.Subject != c.subject {
			t.Fatalf("%s subject = %q, want %q", c.kind, m.Subject, c.subject)
		}
		for _, w := range c.want {
			if !strings.Contains(m.Text, w) {
				t.Fatalf("%s text misses %q:\n%s", c.kind, w, m.Text)
			}
		}
		if !strings.Contains(m.HTML, strings.ReplaceAll(c.p.Link, "&", "&amp;")) || !strings.Contains(m.HTML, "beta.lekalo.studio") {
			t.Fatalf("%s html misses the link or base:\n%s", c.kind, m.HTML)
		}
	}
	// html escapes the team name.
	m, _ := tpl.render(KindInvite, Payload{TeamName: "<b>x</b>", Link: "https://x/join#token=1"}, "x")
	if strings.Contains(m.HTML, "<b>x</b>") {
		t.Fatal("team name not escaped in html")
	}
}

func TestRenderRefusesIncompletePayload(t *testing.T) {
	tpl, err := parseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		kind string
		p    Payload
	}{{KindVerify, Payload{Link: "l"}}, {KindReset, Payload{Link: "l"}}, {KindInvite, Payload{Link: "l"}},
		{KindTeamRequest, Payload{Link: "l", TeamName: "x"}}, {KindTeamApproved, Payload{Link: "l"}},
		{KindRestore, Payload{Link: "l", Code: "123456"}}, {KindRestore, Payload{Link: "l", Until: &time.Time{}}},
		{KindAccountDeleted, Payload{Link: "l"}}, {"nope", Payload{Link: "l", Code: "123456"}}} {
		if _, err := tpl.render(c.kind, c.p, "b"); err == nil {
			t.Fatalf("%s: want an error", c.kind)
		}
	}
	// An error never carries the secret.
	_, err = tpl.render("nope", Payload{Link: "https://x/confirm#token=SECRET", Code: "999111"}, "b")
	if err == nil || strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "999111") {
		t.Fatalf("error leaks a secret: %v", err)
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate(strings.Repeat("a", maxErrorLen+10)); len(got) != maxErrorLen {
		t.Fatalf("len = %d", len(got))
	}
	if truncate("short") != "short" {
		t.Fatal("short changed")
	}
}

func TestPermanentErrorUnwraps(t *testing.T) {
	h := &mail.HTTPSendError{StatusCode: 422, Body: "bad"}
	var e error = &PermanentError{Err: h}
	var got *mail.HTTPSendError
	if !errors.As(e, &got) || got.StatusCode != 422 {
		t.Fatal("unwrap")
	}
}

func TestNewResendSenderNeedsKey(t *testing.T) {
	if _, err := NewResendSender("  "); err == nil {
		t.Fatal("want error without a key")
	}
	if _, err := NewResendSender("re_test"); err != nil {
		t.Fatal(err)
	}
	_ = context.Background()
}
