// Package progress lets long-running lookups report what they are doing.
// A reporter travels in the context; code that has none attached logs
// nothing.
package progress

import (
	"context"
	"fmt"
	"sync"
)

// Func receives one progress message. It may be called from several
// goroutines; With serialises the calls.
type Func func(msg string)

type key struct{}

// With returns a context whose progress messages go to f, one at a time.
func With(ctx context.Context, report Func) context.Context {
	if report == nil {
		return ctx
	}
	var mutex sync.Mutex
	return context.WithValue(ctx, key{}, Func(func(msg string) {
		mutex.Lock()
		defer mutex.Unlock()
		report(msg)
	}))
}

// Logf reports a message to the context's reporter, if there is one.
func Logf(ctx context.Context, format string, args ...any) {
	if report, ok := ctx.Value(key{}).(Func); ok {
		report(fmt.Sprintf(format, args...))
	}
}
