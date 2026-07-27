package credential

import (
	"errors"
	"fmt"
	"strings"
)

var ErrNotFound = errors.New("credential not found")

var allowedNames = map[string]struct{}{
	"inference": {},
	"admin":     {},
	"router":    {},
}

func Target(name string) (string, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if _, ok := allowedNames[name]; !ok {
		return "", fmt.Errorf("unsupported credential %q", name)
	}
	return "CIA Local AI/" + name, nil
}
