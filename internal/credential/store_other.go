//go:build !windows

package credential

import (
	"fmt"
	"os"
	"strings"
)

func Read(name string) (string, error) {
	if _, err := Target(name); err != nil {
		return "", err
	}
	value := os.Getenv("CIA_" + strings.ToUpper(name) + "_TOKEN")
	if value == "" {
		return "", ErrNotFound
	}
	return value, nil
}

func Write(name, value string) error {
	return fmt.Errorf("persistent credential storage is only supported on Windows")
}

func Delete(name string) error {
	return fmt.Errorf("persistent credential storage is only supported on Windows")
}
