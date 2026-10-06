//go:build windows

package daemon

// The daemon needs pseudo-terminals and Unix job control; on Windows atto
// runs its TUI directly.

func Serve(string) error             { return ErrUnavailable }
func List() ([]Pane, error)          { return nil, nil }
func Stop(bool) error                { return ErrUnavailable }
func Kill(string) error              { return ErrUnavailable }
func Run(Hello) (int, string, error) { return 1, "", ErrUnavailable }
