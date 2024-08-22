package healthcheck

import (
	"fmt"
	"io"
)

// CheckObserver receives the results of each check.
type CheckObserver func(*CheckResult)

var (
	// okStatus is a checkmark
	okStatus = "\u221A" // √
	// warStatus is `!!`
	warnStatus = "\u203C" // ‼
	// failStatus is `x`
	failStatus = "\u00D7" // ×
)

// NewObserverPrinter is an observer that prints to an io.write on every
// result from the health checker
func NewObserverPrinter(w io.Writer) CheckObserver {
	return func(result *CheckResult) {
		status := okStatus
		if result.Err != nil {
			status = failStatus
			if result.Warning {
				status = warnStatus
			}
		}

		fmt.Fprintf(w, "[%s] %s/%s\n",
			status,
			result.Category,
			result.Description)

		if result.Err != nil {
			if result.Warning {
				fmt.Fprintf(w, "\tWarning: %s\n", result.Err)
			} else {
				fmt.Fprintf(w, "\tErr: %s\n", result.Err)
			}
			if result.HintURL != "" {
				fmt.Fprintf(w, "\tSee: %s\n", result.HintURL)
			}
		}
	}
}
