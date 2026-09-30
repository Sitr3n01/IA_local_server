//go:build !windows

package monitor

import "time"

func readLaunchInfo(uint32) (launchInfo, bool) { return launchInfo{}, false }

func processStart(uint32) (time.Time, bool) { return time.Time{}, false }
