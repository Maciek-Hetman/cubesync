package sync

import "time"

const defaultInactiveDeviceWindow = 90 * 24 * time.Hour

// effectiveInactiveWindow is shared by retention and the sync cursor-expiry
// check so both agree on which devices are active.
func effectiveInactiveWindow(d time.Duration) time.Duration {
	if d <= 0 {
		return defaultInactiveDeviceWindow
	}
	return d
}
