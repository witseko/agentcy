package shim

import (
	"os"
	"strings"
	"testing"
	"testing/fstest"
)

func TestEmbeddedSkillsEqualThePythonOriginals(t *testing.T) {
	for _, name := range packagedSkills {
		original, err := os.ReadFile("../../src/agentcy/mcp/skills/" + name + "/SKILL.md")
		if err != nil {
			t.Fatal(err)
		}
		embedded, err := skillFiles.ReadFile("skills/" + name + "/SKILL.md")
		if err != nil || string(embedded) != string(original) {
			t.Errorf("skill %s differs from src/agentcy/mcp/skills (err %v)", name, err)
		}
	}
	icon, err := os.ReadFile("../../src/agentcy/mcp/icon.png")
	if err != nil || string(icon) != string(serverIcon) {
		t.Errorf("icon differs from src/agentcy/mcp/icon.png (err %v)", err)
	}
}

func TestSkillsStayWithinHostLimits(t *testing.T) {
	skills, err := loadPackagedSkills()
	if err != nil {
		t.Fatal(err)
	}
	for _, packaged := range skills {
		total := 0
		for _, file := range packaged.files {
			total += file.size
			if file.size > maxSkillFileBytes || !strings.HasPrefix(file.digest, "sha256:") {
				t.Errorf("%s: size %d digest %s", file.uri, file.size, file.digest)
			}
		}
		if total > maxSkillBytes || packaged.uri != "skill://"+packaged.name+"/SKILL.md" {
			t.Errorf("skill %s: %d bytes, uri %s", packaged.name, total, packaged.uri)
		}
	}
}

func TestOversizedSkillFileFailsAtLoad(t *testing.T) {
	files := fstest.MapFS{"skills/demo/SKILL.md": {Data: []byte("---\nname: demo\ndescription: Demo.\n---\n" + strings.Repeat("x", maxSkillFileBytes))}}
	if _, err := loadSkill(files, "demo"); err == nil || !strings.Contains(err.Error(), "exceeds 100000 bytes") {
		t.Errorf("err = %v", err)
	}
}

func TestSkillNameMustEqualDirectory(t *testing.T) {
	if err := validateFrontmatter("other", "Text.", "lead-conversation"); err == nil || !strings.Contains(err.Error(), "frontmatter name") {
		t.Errorf("err = %v", err)
	}
}

func TestFrontmatterParserReadsPlainAndFoldedValues(t *testing.T) {
	fields, err := parseFrontmatter("---\nname: demo\ndescription: >-\n  First line.\n  Second line.\n---\n# Body\n")
	if err != nil {
		t.Fatal(err)
	}
	if got := fields.Compact(); got != `{"name":"demo","description":"First line. Second line."}` {
		t.Errorf("fields %s", got)
	}
}

func TestFrontmatterParserRejectsOtherYAML(t *testing.T) {
	for _, frontmatter := range []string{
		"name: demo\ndescription: 'quoted'",
		"name: demo\nallowed-tools:\n  - Bash",
		"name: demo\ndescription: >-\n  One.\n\n  Two.",
		"name: demo\ndescription: >-\n  One.\n    Two.",
		"name: demo\nversion: 1",
		"name: demo\nenabled: true",
		"name: demo\ndescription: a: b",
		"name: demo\nname: again",
	} {
		if _, err := parseFrontmatter("---\n" + frontmatter + "\n---\n"); err == nil {
			t.Errorf("accepted %q", frontmatter)
		}
	}
}
