package shim

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/witseko/agentcy/internal/wire"
)

// The packaged skills.
//
//go:embed skills
var skillFiles embed.FS

// Skills extension constants, from the former Python core's mcp/skills.py.
const (
	skillsExtension      = "io.modelcontextprotocol/skills"
	skillFile            = "SKILL.md"
	markdownType         = "text/markdown"
	maxSkillFileBytes    = 100_000
	maxSkillBytes        = 400_000
	maxNameLength        = 64
	maxDescriptionLength = 1024
	skillCacheTTL        = 300_000
	frontmatterFence     = "---"
	foldedMarker         = ">-"
)

// packagedSkills lists the shipped skills in the order they are served.
var packagedSkills = []string{"lead-conversation", "join-conversation"}

var (
	skillNamePattern   = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	frontmatterField   = regexp.MustCompile(`^([a-z][a-z0-9_-]*): (.+)$`)
	yamlIndicators     = "'\"[]{}>|*&!%@#`,?:-"
	nonStringYAMLWords = map[string]bool{"true": true, "false": true, "yes": true, "no": true, "on": true, "off": true, "null": true, "~": true}
)

// skillResource is one served file of a skill, with the digest and size of its bytes.
type skillResource struct {
	uri    string
	text   string
	digest string
	size   int
}

// skill is one packaged skill: its SKILL.md URI, its frontmatter in file order, and all
// of its files.
type skill struct {
	uri         string
	name        string
	description string
	frontmatter wire.Value
	files       []skillResource
}

// entry returns the wire form of a skill for skills/list and skills/get.
func (packaged skill) entry() wire.Value {
	entry := wire.NewObject()
	entry.Set("uri", wire.TextValue(packaged.uri))
	entry.Set("frontmatter", packaged.frontmatter.Copy())
	resources := make([]wire.Value, 0, len(packaged.files))
	for _, file := range packaged.files {
		resource := wire.NewObject()
		resource.Set("uri", wire.TextValue(file.uri))
		resource.Set("digest", wire.TextValue(file.digest))
		resource.Set("size", wire.IntValue(int64(file.size)))
		resources = append(resources, resource)
	}
	entry.Set("resources", wire.ListValue(resources))
	return entry
}

// loadPackagedSkills loads and validates every shipped skill.
func loadPackagedSkills() ([]skill, error) {
	skills := make([]skill, 0, len(packagedSkills))
	for _, name := range packagedSkills {
		loaded, err := loadSkill(skillFiles, name)
		if err != nil {
			return nil, err
		}
		skills = append(skills, loaded)
	}
	return skills, nil
}

// loadSkill reads one skill directory, SKILL.md first and the other files sorted.
func loadSkill(files fs.FS, name string) (skill, error) {
	root := "skills/" + name
	var paths []string
	err := fs.WalkDir(files, root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			paths = append(paths, strings.TrimPrefix(path, root+"/"))
		}
		return nil
	})
	if err != nil {
		return skill{}, err
	}
	sort.Slice(paths, func(first, second int) bool {
		if (paths[first] == skillFile) != (paths[second] == skillFile) {
			return paths[first] == skillFile
		}
		return paths[first] < paths[second]
	})
	if len(paths) == 0 || paths[0] != skillFile {
		return skill{}, fmt.Errorf("skill %q has no %s", name, skillFile)
	}
	loaded := skill{}
	total := 0
	for _, path := range paths {
		file, err := loadSkillFile(files, root, name, path)
		if err != nil {
			return skill{}, err
		}
		total += file.size
		loaded.files = append(loaded.files, file)
	}
	if total > maxSkillBytes {
		return skill{}, fmt.Errorf("skill %q exceeds %d bytes", name, maxSkillBytes)
	}
	frontmatter, err := parseFrontmatter(loaded.files[0].text)
	if err != nil {
		return skill{}, err
	}
	loaded.uri = loaded.files[0].uri
	loaded.frontmatter = frontmatter
	loaded.name = textField(frontmatter, "name")
	loaded.description = textField(frontmatter, "description")
	return loaded, validateFrontmatter(loaded.name, loaded.description, name)
}

// loadSkillFile reads one Markdown file of a skill.
func loadSkillFile(files fs.FS, root, name, path string) (skillResource, error) {
	if !strings.HasSuffix(path, ".md") {
		return skillResource{}, fmt.Errorf("skill %q file %q is not Markdown", name, path)
	}
	data, err := fs.ReadFile(files, root+"/"+path)
	if err != nil {
		return skillResource{}, err
	}
	if len(data) > maxSkillFileBytes {
		return skillResource{}, fmt.Errorf("skill %q file %q exceeds %d bytes", name, path, maxSkillFileBytes)
	}
	if !utf8.Valid(data) {
		return skillResource{}, fmt.Errorf("skill %q file %q is not UTF-8", name, path)
	}
	digest := sha256.Sum256(data)
	return skillResource{
		uri:    fmt.Sprintf("skill://%s/%s", name, path),
		text:   string(data),
		digest: "sha256:" + hex.EncodeToString(digest[:]),
		size:   len(data),
	}, nil
}

// textField reads one string member of an object.
func textField(object wire.Value, key string) string {
	value, _ := object.Get(key)
	return value.StringOf()
}

// validateFrontmatter checks the required fields and that the name equals the directory.
func validateFrontmatter(name, description, directory string) error {
	if name != directory {
		return fmt.Errorf("skill %q has frontmatter name %q", directory, name)
	}
	if !skillNamePattern.MatchString(name) || utf8.RuneCountInString(name) > maxNameLength {
		return fmt.Errorf("skill name %q breaks the Agent Skills naming rules", name)
	}
	if description == "" || utf8.RuneCountInString(description) > maxDescriptionLength {
		return fmt.Errorf("skill %q needs a description of 1 to 1024 characters", name)
	}
	return nil
}

// splitLines splits text like Python's str.splitlines for the line breaks a Markdown
// file can hold: "\n", "\r\n" and "\r".
func splitLines(text string) []string {
	normalized := strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	lines := strings.Split(normalized, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// parseFrontmatter parses the YAML frontmatter of a SKILL.md file. Like the Python
// parser, it accepts only `key: value` lines and `key: >-` with one indented paragraph,
// and returns the fields in file order.
func parseFrontmatter(text string) (wire.Value, error) {
	lines := splitLines(text)
	closing := -1
	for index := 1; index < len(lines); index++ {
		if lines[index] == frontmatterFence {
			closing = index
			break
		}
	}
	if len(lines) == 0 || lines[0] != frontmatterFence || closing < 0 {
		return wire.Value{}, fmt.Errorf("SKILL.md must start with a frontmatter block between '---' lines")
	}
	body := lines[1:closing]
	fields := wire.NewObject()
	for index := 0; index < len(body); {
		match := frontmatterField.FindStringSubmatch(body[index])
		if match == nil {
			return wire.Value{}, fmt.Errorf("frontmatter line %q is not 'key: value'", body[index])
		}
		key, value := match[1], match[2]
		if fields.Has(key) {
			return wire.Value{}, fmt.Errorf("frontmatter field %q appears twice", key)
		}
		if value == foldedMarker {
			folded, next, err := foldedValue(body, index+1, key)
			if err != nil {
				return wire.Value{}, err
			}
			fields.Set(key, wire.TextValue(folded))
			index = next
			continue
		}
		plain, err := plainValue(value, key)
		if err != nil {
			return wire.Value{}, err
		}
		fields.Set(key, wire.TextValue(plain))
		index++
	}
	return fields, nil
}

// plainValue returns a one-line plain YAML value, or an error if YAML would read it
// as something other than this string.
func plainValue(value, key string) (string, error) {
	_, numberErr := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if strings.ContainsAny(value[:1], yamlIndicators) || strings.Contains(value, ": ") ||
		strings.Contains(value, " #") || strings.HasSuffix(value, ":") ||
		nonStringYAMLWords[strings.ToLower(value)] || numberErr == nil {
		return "", fmt.Errorf("frontmatter field %q needs YAML features this parser rejects", key)
	}
	return value, nil
}

// foldedValue joins the indented lines of a `>-` block into one string and returns the
// index of the first line after the block.
func foldedValue(lines []string, start int, key string) (string, int, error) {
	end := start
	for end < len(lines) && strings.HasPrefix(lines[end], " ") {
		end++
	}
	block := lines[start:end]
	if len(block) == 0 {
		return "", 0, fmt.Errorf("frontmatter field %q has an empty folded block", key)
	}
	indent := len(block[0]) - len(strings.TrimLeft(block[0], " "))
	parts := make([]string, 0, len(block))
	for _, line := range block {
		if strings.TrimSpace(line) == "" || len(line)-len(strings.TrimLeft(line, " ")) != indent {
			return "", 0, fmt.Errorf("frontmatter field %q needs YAML features this parser rejects", key)
		}
		parts = append(parts, strings.TrimSpace(line))
	}
	return strings.Join(parts, " "), end, nil
}
