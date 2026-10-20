//go:build !darwin && !(linux && (amd64 || arm64))

package guestlayout

import "fmt"

func publishDirectory(_, _ string) error {
	return fmt.Errorf("atomic exclusive directory publication requires macOS or Linux amd64/arm64")
}
