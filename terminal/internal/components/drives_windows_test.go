package components

import (
	"os"
	"slices"
	"strings"
	"testing"
)

// The drive row is built without opening any drive. A mapped drive whose server
// cannot be reached would otherwise hold the event loop, so no key works, for as
// long as Windows waits on the share, once per such drive.
func TestDrivesOpenNoDrive(t *testing.T) {
	saved := logicalDrives
	t.Cleanup(func() { logicalDrives = saved })

	// Every letter in use, though most do not exist here: a listing that looked at
	// the drives would leave them out.
	logicalDrives = func() (uint32, error) { return 1<<26 - 1, nil }
	got := Drives("windows")
	if len(got) != 26 || got[0] != `A:\` || got[25] != `Z:\` {
		t.Errorf("drives %q", got)
	}

	logicalDrives = saved
	drive := os.Getenv("SystemDrive")
	if drive == "" {
		return
	}
	system := strings.ToUpper(drive) + `\`
	if real := Drives("windows"); !slices.Contains(real, system) {
		t.Errorf("drives %q lack the system drive %q", real, system)
	}
}
