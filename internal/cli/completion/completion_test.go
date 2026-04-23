package completion

import (
	"strings"
	"testing"
)

func TestScripts_substitutePlaceholder(t *testing.T) {
	for shell, tmpl := range scripts {
		t.Run(shell, func(t *testing.T) {
			rendered := Render(tmpl, "esops")

			if strings.Contains(rendered, "{{APP}}") {
				t.Errorf("%s: rendered script still contains placeholder:\n%s", shell, rendered)
			}
			// pwsh derives the name from the script filename, so it has no
			// placeholder by design. Every other shell must mention the app.
			if shell != "pwsh" && !strings.Contains(rendered, "esops") {
				t.Errorf("%s: rendered script missing app name:\n%s", shell, rendered)
			}
			// Guard against fmt-style artefacts from the upstream v3.8.0 bug.
			for _, artefact := range []string{"%!(", "%[1]", "BADWIDTH", "MISSING"} {
				if strings.Contains(rendered, artefact) {
					t.Errorf("%s: rendered script contains fmt artefact %q:\n%s", shell, artefact, rendered)
				}
			}
		})
	}
}

func TestRender_respectsCustomAppName(t *testing.T) {
	out := Render(scripts["bash"], "myops")
	if !strings.Contains(out, "__myops_bash_autocomplete") {
		t.Errorf("expected rendered bash script to use custom app name, got:\n%s", out)
	}
	if strings.Contains(out, "esops") {
		t.Errorf("rendered script leaked the default app name:\n%s", out)
	}
}
