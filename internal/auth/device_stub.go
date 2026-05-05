package auth

import (
	"context"
	"errors"
)

// LoginDevice runs the OAuth Device Authorization Grant flow.
//
// PLACEHOLDER: this file exists so internal/commands/login.go can reference
// the symbol while Task 3 is in flight. Task 4 will replace the body with the
// real polling implementation. Until then, callers receive a clear error.
func LoginDevice(_ context.Context, _ LoopbackConfig) (Tokens, error) {
	return Tokens{}, errors.New("device flow not yet implemented (lands in Task 4)")
}
