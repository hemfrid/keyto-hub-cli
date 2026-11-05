package envsync

import "github.com/hemfrid/keyto-hub-cli/internal/safefile"

// Test-only handles on unexported helpers.
var (
	QuoteDevVarsValue = quoteDevVarsValue
	TempFileName      = safefile.TempFileName
)
