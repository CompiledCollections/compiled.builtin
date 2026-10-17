package ansible

import (
	"regexp"
	"sync"
)

// LazyRegexp compiles its expression on first use. Unlike a package-level
// regexp.MustCompile, it keeps the regexp engine out of binaries that never
// use it.
type LazyRegexp struct {
	Expr string
	once sync.Once
	re   *regexp.Regexp
}

// Get returns the compiled expression.
func (l *LazyRegexp) Get() *regexp.Regexp {
	l.once.Do(func() { l.re = regexp.MustCompile(l.Expr) })
	return l.re
}
