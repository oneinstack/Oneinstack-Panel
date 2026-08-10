//go:build !linux

package ssh

import "os"

// Production terminal sessions run on Linux. Other platforms fail closed so
// local development can never persist input whose echo state is unknown.
func terminalInputVisible(*os.File) bool {
	return false
}
