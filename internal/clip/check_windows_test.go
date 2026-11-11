//go:build windows

package clip

import "testing"

func TestCheckAvailableWindows(t *testing.T) {
	if err := CheckAvailable(); err != nil {
		t.Fatalf("Windows 上自检恒为 nil，实际: %v", err)
	}
}
