package components

import "golang.org/x/sys/windows"

// logicalDrives is the system call, replaced in tests.
var logicalDrives = windows.GetLogicalDrives

// driveRoots asks Windows which letters are in use. It touches no drive, because
// this runs on the event loop and a mapped drive that has gone away blocks anything
// that opens it for many seconds. Such a drive is listed, and the file panel reports
// it when it is chosen.
func driveRoots() []string {
	mask, err := logicalDrives()
	if err != nil {
		return nil
	}
	var out []string
	for letter := range 26 {
		if mask&(1<<letter) != 0 {
			out = append(out, string(rune('A'+letter))+`:\`)
		}
	}
	return out
}
