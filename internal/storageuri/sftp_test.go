package storageuri

import "testing"

func TestParseSFTP(t *testing.T) {
	for _, value := range []string{"sftp://host", "sftp://host/../escape", "sftp://host/%2e%2e/escape", "sftp://u:password@host/root", "sftp://host:0/root", "sftp://host:65536/root", "sftp://host/root?q=x", "sftp://host/root#x", "sftp://host/root%00x", "sftp://host/root%5cx"} {
		if _, err := ParseSFTP(value); err == nil {
			t.Errorf("accepted %q", value)
		}
	}
	u, err := ParseSFTP("sftp://user@HOST/root/a%20b/")
	if err != nil || u.String() != "sftp://user@host:22/root/a%20b" {
		t.Fatalf("canonical URI: %v %v", u, err)
	}
	u, err = ParseSFTP("sftp://[::1]:2222/root")
	if err != nil || u.Host != "[::1]:2222" {
		t.Fatalf("IPv6: %v %v", u, err)
	}
}
