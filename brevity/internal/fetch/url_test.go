package fetch

import "testing"

func TestValidateHTTPURLBlocksLocalHosts(t *testing.T) {
	for _, raw := range []string{
		"http://localhost/test",
		"http://127.0.0.1/test",
		"http://10.0.0.1/test",
		"http://169.254.169.254/latest/meta-data",
	} {
		if _, err := validateHTTPURL(raw); err == nil {
			t.Fatalf("expected %s to be blocked", raw)
		}
	}
}

func TestValidateHTTPURLAllowsPublicHTTPS(t *testing.T) {
	if _, err := validateHTTPURL("https://example.com/article"); err != nil {
		t.Fatalf("expected public URL to pass: %v", err)
	}
}
