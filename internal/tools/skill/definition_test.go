package skill

import (
	"strings"
	"testing"
)

func TestSkillToolDescriptionDistinguishesReferences(t *testing.T) {
	if !strings.Contains(SkillToolDescription, "top-level") {
		t.Fatalf("expected top-level skill guidance in description: %q", SkillToolDescription)
	}
	if !strings.Contains(SkillToolDescription, "Read tool by file path") {
		t.Fatalf("expected reference loading guidance in description: %q", SkillToolDescription)
	}
	if !strings.Contains(SkillToolDescription, "not with load_skill") {
		t.Fatalf("expected load_skill reference prohibition in description: %q", SkillToolDescription)
	}
}
