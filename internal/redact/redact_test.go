package redact_test

import (
	"strings"
	"testing"

	"github.com/mattiasthalen/qlik-parser/internal/redact"
)

func kinds(fs []redact.Finding) map[string]int {
	m := map[string]int{}
	for _, f := range fs {
		m[f.Kind]++
	}
	return m
}

func TestScan_PasswordInConnectionString(t *testing.T) {
	script := "LIB CONNECT TO [Provider=MSOLEDBSQL;Data Source=srv;User ID=sa;Password=S3cr3tP@ss;];"
	fs := redact.Scan(script)
	if kinds(fs)["password"] == 0 {
		t.Fatalf("expected a password finding, got %+v", fs)
	}
	for _, f := range fs {
		if strings.Contains(f.Preview, "S3cr3tP@ss") {
			t.Errorf("preview leaked the secret: %q", f.Preview)
		}
	}
}

func TestApply_RedactReplacesValueKeepsKey(t *testing.T) {
	script := "Password=SuperSecret1;\napikey: 'abcd1234efgh';"
	out, fs := redact.Apply(script, redact.ModeRedact)
	if strings.Contains(out, "SuperSecret1") || strings.Contains(out, "abcd1234efgh") {
		t.Errorf("secret not redacted: %q", out)
	}
	if !strings.Contains(out, "Password=") {
		t.Errorf("key should be preserved: %q", out)
	}
	if !strings.Contains(out, redact.Placeholder) {
		t.Errorf("placeholder missing: %q", out)
	}
	if len(fs) != 2 {
		t.Errorf("expected 2 findings, got %d: %+v", len(fs), fs)
	}
}

func TestApply_FlagLeavesTextUnchanged(t *testing.T) {
	script := "pwd='hunter2xyz';"
	out, fs := redact.Apply(script, redact.ModeFlag)
	if out != script {
		t.Errorf("flag mode must not modify text: %q", out)
	}
	if len(fs) != 1 || fs[0].Kind != "password" {
		t.Errorf("expected 1 password finding, got %+v", fs)
	}
}

func TestScan_URLCredentials(t *testing.T) {
	script := "LET vUrl = 'https://user:p4ssw0rd@example.com/api';"
	fs := redact.Scan(script)
	if kinds(fs)["urlCredentials"] == 0 {
		t.Errorf("expected urlCredentials finding, got %+v", fs)
	}
	out, _ := redact.Apply(script, redact.ModeRedact)
	if strings.Contains(out, "p4ssw0rd") {
		t.Errorf("url password not redacted: %q", out)
	}
	// host and user should survive
	if !strings.Contains(out, "user:") || !strings.Contains(out, "@example.com") {
		t.Errorf("redaction damaged the URL: %q", out)
	}
}

func TestScan_AWSAndPrivateKey(t *testing.T) {
	script := "key=AKIAIOSFODNN7EXAMPLE\n-----BEGIN RSA PRIVATE KEY-----\nMIIABC\n-----END RSA PRIVATE KEY-----"
	fs := redact.Scan(script)
	k := kinds(fs)
	if k["awsAccessKey"] == 0 {
		t.Errorf("expected awsAccessKey finding, got %+v", fs)
	}
	if k["privateKey"] == 0 {
		t.Errorf("expected privateKey finding, got %+v", fs)
	}
}

func TestScan_LineNumbers(t *testing.T) {
	script := "line1\nline2\nPassword=abcdef;\n"
	fs := redact.Scan(script)
	if len(fs) != 1 || fs[0].Line != 3 {
		t.Errorf("expected finding on line 3, got %+v", fs)
	}
}

func TestScan_NoSecrets(t *testing.T) {
	fs := redact.Scan("LOAD Field1, Field2 FROM [lib://data/x.qvd] (qvd);")
	if len(fs) != 0 {
		t.Errorf("expected no findings, got %+v", fs)
	}
}

func TestApply_NoDoubleReporting(t *testing.T) {
	// "password=" matches both the quoted and unquoted patterns; must report once.
	script := "Password='abcdef123';"
	fs := redact.Scan(script)
	if len(fs) != 1 {
		t.Errorf("expected exactly 1 finding, got %d: %+v", len(fs), fs)
	}
}

func TestWarnings(t *testing.T) {
	fs := []redact.Finding{{Kind: "password", Line: 5, Preview: "ab****"}}
	w := redact.Warnings("script", fs)
	if len(w) != 1 || !strings.Contains(w[0], "password") || !strings.Contains(w[0], "line 5") {
		t.Errorf("unexpected warnings: %v", w)
	}
}
