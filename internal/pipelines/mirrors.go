package pipelines

import "strings"

// ImageMirror sends the images of one registry through another, a pull-through cache: with
// `ghcr.io` mirrored by `registry.example.org/ghcr`, `ghcr.io/org/tool:v1` is pulled as
// `registry.example.org/ghcr/org/tool:v1`.
type ImageMirror struct {
	Registry string
	Mirror   string
}

// mirrorImages rewrites every string of a definition's spec that is a reference to an image of a
// mirrored registry.
//
// Every string, and not only the `image` fields: an image also travels as a parameter's value or
// default (`gitInitImage`), and nothing in a parameter says that it names one. A reference is the
// whole string, so a script that mentions a registry keeps its text.
func mirrorImages(value any, mirrors []ImageMirror) any {
	switch typed := value.(type) {
	case string:
		for _, mirror := range mirrors {
			if rest, ok := strings.CutPrefix(typed, mirror.Registry+"/"); ok {
				return mirror.Mirror + "/" + rest
			}
		}
	case map[string]any:
		for key, entry := range typed {
			typed[key] = mirrorImages(entry, mirrors)
		}
	case []any:
		for index, entry := range typed {
			typed[index] = mirrorImages(entry, mirrors)
		}
	}
	return value
}
