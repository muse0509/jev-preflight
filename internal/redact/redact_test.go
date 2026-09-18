package redact

import (
	"strings"
	"testing"
)

func TestCategories(t *testing.T) {
	secret := "fixture-" + strings.Repeat("x", 24)
	for _, tt := range []struct{ name, input, secret, marker string }{
		{"pem", "-----BEGIN RSA PRIVATE KEY-----\n+" + secret + "\n+-----END RSA PRIVATE KEY-----", secret, "private-key"},
		{"pem generic", "-----BEGIN PRIVATE KEY-----\n" + secret + "\n-----END PRIVATE KEY-----", secret, "private-key"},
		{"aws", "id: " + "AK" + "IA" + strings.Repeat("A", 16), "AK" + "IA" + strings.Repeat("A", 16), "aws-key"},
		{"bearer", "use Bearer " + secret, secret, "token"},
		{"api token", "value: " + "sk-" + strings.Repeat("x", 24), "sk-" + strings.Repeat("x", 24), "token"},
		{"github", "value: " + "gh" + "p_" + strings.Repeat("x", 24), "gh" + "p_" + strings.Repeat("x", 24), "token"},
		{"password", "+password = \"" + secret + "\"", secret, "assignment"},
		{"json key", `+"api_key": "` + secret + `",`, secret, "assignment"},
		{"camel key", "+apiKey: '" + secret + "'", secret, "assignment"},
		{"env token", "+SERVICE_TOKEN=" + secret, secret, "assignment"},
		{"secret", "+client_secret: " + secret, secret, "assignment"},
		{"auth", "+Authorization: Basic " + secret, secret, "authorization"},
		{"auth json", `+"Authorization": "Bearer ` + secret + `"`, secret, "authorization"},
		{"cookie", "+Cookie: session=" + secret + "; theme=dark", secret, "cookie"},
		{"set cookie", "+Set-Cookie: session=" + secret + "; Secure", secret, "cookie"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := Text(tt.input)
			if strings.Contains(got, tt.secret) || !strings.Contains(got, "[REDACTED:"+tt.marker+"]") {
				t.Fatalf("redaction failed for %s", tt.name)
			}
		})
	}
}

func TestNonSecretPreservation(t *testing.T) {
	s := "diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n-old := 1\n+value := 42\n+message := \"hello\"\n"
	if Text(s) != s {
		t.Fatal("ordinary diff changed")
	}
}

func TestQuotedMultilineAssignments(t *testing.T) {
	for _, quote := range []string{"`", `"""`, "'''"} {
		value := "synthetic private value\nwith another line"
		input := "+secret = " + quote + value + quote + "\n+return true\n"
		got := Text(input)
		if strings.Contains(got, "synthetic") || strings.Contains(got, "another line") || !strings.Contains(got, "+return true") {
			t.Errorf("quoted secret not removed for delimiter %q", quote)
		}
	}
}
