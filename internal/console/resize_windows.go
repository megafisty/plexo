//go:build windows

package console

import "os"

// resizeSignals has no Windows equivalent; the periodic redraw covers resizes.
func resizeSignals() <-chan os.Signal { return nil }
