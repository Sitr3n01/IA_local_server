//go:build !windows

package edge

func systemMemoryStatus() (memorySnapshot, error) {
	return memorySnapshot{}, errCapacityUnavailable
}
