package common

import "testing"

func TestStorageSizeTerminalString(t *testing.T) {
	cases := []struct {
		size StorageSize
		want string
	}{
		{500, "500.00B"},
		{2 * storageKiB, "2.00KiB"},
		{2 * storageMiB, "2.00MiB"},
		{2 * storageGiB, "2.00GiB"},
		{2 * storageTiB, "2.00TiB"},
	}
	for _, c := range cases {
		if got := c.size.TerminalString(); got != c.want {
			t.Errorf("TerminalString(%v) = %q, want %q", float64(c.size), got, c.want)
		}
	}
}
