package service

import "testing"

func TestInventoryNameFromURI(t *testing.T) {
	tests := []struct {
		uri  string
		want string
	}{
		{uri: "/home/username/images/Vacation March 2026", want: "Vacation March 2026"},
		{uri: "/home/username/images/Vacation March 2026/", want: "Vacation March 2026"},
		{uri: "s3://photo-bucket/album-one", want: "album-one"},
		{uri: `C:\photos\summer`, want: "summer"},
	}

	for _, test := range tests {
		if got := inventoryNameFromURI(test.uri); got != test.want {
			t.Fatalf("inventoryNameFromURI(%q) = %q, want %q", test.uri, got, test.want)
		}
	}
}
