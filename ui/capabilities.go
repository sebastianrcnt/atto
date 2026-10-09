package ui

import "fmt"

type Capabilities struct {
	Version  int      `json:"version"`
	Surface  string   `json:"surface"`
	Width    int      `json:"width"`
	Elements []string `json:"elements"`
}

func (c Capabilities) Validate() error {
	if c.Version != 1 || c.Width < 0 || c.Width > 10000 {
		return fmt.Errorf("invalid UI version/width")
	}
	switch c.Surface {
	case "terminal", "web", "gui", "flutter", "headless":
	default:
		return fmt.Errorf("invalid UI surface")
	}
	if len(c.Elements) > 256 {
		return fmt.Errorf("too many elements")
	}
	return nil
}
func Catalog() []string {
	return []string{"Box", "Text", "Markdown", "Code", "Diff", "Link", "Button", "Input", "Select", "List", "Progress", "Collapse", "Image"}
}
