//go:build !windows

package edge

func systemCommitHeadroomGiB() (float64, error) {
	return 0, errCapacityUnavailable
}
