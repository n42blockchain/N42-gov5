package paths

import "testing"

func TestWindowsAppDataMissingEnvReturnsEmpty(t *testing.T) {
	t.Setenv("LOCALAPPDATA", "")

	if got := windowsAppData(); got != "" {
		t.Fatalf("windowsAppData() = %q, want empty string", got)
	}
}

func TestWindowsAppDataReturnsConfiguredValue(t *testing.T) {
	t.Setenv("LOCALAPPDATA", `C:\Users\Test\AppData\Local`)

	if got := windowsAppData(); got != `C:\Users\Test\AppData\Local` {
		t.Fatalf("windowsAppData() = %q", got)
	}
}
