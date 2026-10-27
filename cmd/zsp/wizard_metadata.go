package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zapstore/zsp/v2"
	"github.com/zapstore/zsp/v2/internal/config"
	"github.com/zapstore/zsp/v2/internal/source"
	"github.com/zapstore/zsp/v2/internal/ui"
	"gopkg.in/yaml.v3"
)

const wizardConfigName = "zapstore.yaml"

// metadataSourceOrder is the order a listing reads sources. The first source
// with a value wins for each field.
var metadataSourceOrder = []string{"fastlane", "github", "gitlab", "gitea", "fdroid", "playstore"}

// wizardChooseMetadata asks which discovered sources should fill the listing,
// then writes that choice into zapstore.yaml. It returns true when Esc should
// return to the APK prompt. An explicit metadata_sources list, including an
// empty one, is left as it is.
func wizardChooseMetadata(ctx context.Context, run *wizardRun, packageID string) (bool, error) {
	if run.config.Publish.MetadataSources != nil {
		return false, nil
	}
	spinner := ui.NewSpinner("Looking for listing text and screenshots...")
	spinner.Start()
	found, err := source.NewMetadataFetcherWithPackageID(wizardMetadataConfig(run), packageID).AvailableSources(ctx)
	spinner.Stop()
	if err != nil {
		return false, err
	}
	set := false
	var selected []string
	if len(found) > 0 {
		selected, err = ui.MultiSelectFieldBack(ctx, "Where should the listing text and screenshots come from?", wizardMetadataDescription(found), wizardMetadataChoices(found))
		if errors.Is(err, ui.ErrBack) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		selected = orderMetadataSources(selected)
		set = true
	} else {
		ui.PrintInfo("Listing name and icon will come from the APK.")
	}
	existed := run.localYAML
	path, err := run.saveMetadataSources(selected, set)
	if err != nil {
		return false, err
	}
	if path == "" {
		return false, nil
	}
	if existed {
		ui.PrintInfo("Updated zapstore.yaml (" + path + ")")
		return false, nil
	}
	ui.PrintInfo("Wrote zapstore.yaml (" + path + ")")
	return false, nil
}

func wizardMetadataChoices(sources []string) []ui.SelectChoice {
	choices := make([]ui.SelectChoice, len(sources))
	for i, name := range sources {
		choices[i] = ui.SelectChoice{Label: metadataSourceLabel(name), Value: name}
	}
	return choices
}

func wizardMetadataDescription(sources []string) string {
	phrases := make([]string, len(sources))
	for i, name := range sources {
		phrases[i] = metadataSourcePhrase(name)
	}
	return "Found " + humanList(phrases) + "."
}

func metadataSourceLabel(name string) string {
	switch name {
	case "fastlane":
		return "Fastlane"
	case "github":
		return "GitHub"
	case "gitlab":
		return "GitLab"
	case "gitea":
		return "Gitea"
	case "fdroid":
		return "F-Droid"
	case "playstore":
		return "Play Store"
	default:
		return name
	}
}

func metadataSourcePhrase(name string) string {
	switch name {
	case "playstore":
		return "a Play Store listing"
	case "fdroid":
		return "an F-Droid package page"
	default:
		return metadataSourceLabel(name)
	}
}

func humanList(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	case 2:
		return items[0] + " and " + items[1]
	default:
		return strings.Join(items[:len(items)-1], ", ") + ", and " + items[len(items)-1]
	}
}

// orderMetadataSources returns selected names in metadataSourceOrder. The
// result is empty and non-nil when nothing was kept, which writes [].
func orderMetadataSources(selected []string) []string {
	ordered := make([]string, 0, len(selected))
	seen := make(map[string]struct{}, len(selected))
	for _, name := range metadataSourceOrder {
		for _, chosen := range selected {
			if chosen != name {
				continue
			}
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			ordered = append(ordered, name)
		}
	}
	return ordered
}

func wizardMetadataConfig(run *wizardRun) *config.Config {
	cfg := &config.Config{Repository: run.config.Fetch.Repository}
	if run.localYAML && run.configLabel != "" {
		cfg.BaseDir = filepath.Dir(run.configLabel)
	} else if wd, err := os.Getwd(); err == nil {
		cfg.BaseDir = wd
	}
	if src := run.config.Fetch.ReleaseSource; src != nil {
		cfg.ReleaseSource = &config.ReleaseSource{
			URL:         src.URL,
			LocalPath:   src.LocalPath,
			Type:        src.Type,
			AssetURL:    src.AssetURL,
			IsWebSource: src.AssetURL != "" || src.VersionExtractor != nil || src.AssetExtractor != nil,
		}
	}
	return cfg
}

// saveMetadataSources writes zapstore.yaml and records the choice on run.
// set reports that the developer was shown the prompt, so an empty sources
// list is an explicit metadata_sources: []. A path is returned when a file
// was written.
func (run *wizardRun) saveMetadataSources(sources []string, set bool) (string, error) {
	path := wizardConfigWritePath(run)
	var data []byte
	var err error
	switch {
	case len(run.yamlBytes) == 0:
		data, err = newWizardYAML(run.config.Fetch, sources, set)
	case run.sourceChanged:
		data, err = rewriteWizardSource(run.yamlBytes, run.config.Fetch, sources, set)
	case set:
		data = appendMetadataSources(run.yamlBytes, sources)
	default:
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if err := writeWizardFile(path, data); err != nil {
		return "", err
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	run.configLabel = absolute
	run.localYAML = true
	run.yamlBytes = data
	run.wroteYAML = true
	if set {
		run.config.Publish.MetadataSources = append([]string{}, sources...)
	}
	return absolute, nil
}

func wizardConfigWritePath(run *wizardRun) string {
	if run.localYAML && run.configLabel != "" {
		return run.configLabel
	}
	return wizardConfigName
}

func newWizardYAML(fetch zsp.FetchConfig, sources []string, set bool) ([]byte, error) {
	cfg := config.Config{Repository: fetch.Repository}
	if src := fetch.ReleaseSource; src != nil {
		if value := wizardReleaseSourceValue(src); value != "" {
			if src.LocalPath != "" {
				cfg.ReleaseSource = &config.ReleaseSource{LocalPath: value}
			} else {
				cfg.ReleaseSource = &config.ReleaseSource{URL: value, Type: src.Type}
			}
		}
	}
	if cfg.Repository == "" && cfg.ReleaseSource == nil {
		return nil, fmt.Errorf("write zapstore.yaml: repository or release source is required")
	}
	if set {
		cfg.MetadataSources = append([]string{}, sources...)
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("write zapstore.yaml: %w", err)
	}
	return data, nil
}

func wizardReleaseSourceValue(src *zsp.ReleaseSource) string {
	if src == nil {
		return ""
	}
	if src.LocalPath == "" {
		return src.URL
	}
	return relativeToWorkingDir(src.LocalPath)
}

// relativeToWorkingDir returns path relative to the working directory when it
// stays inside that directory. Abs and Getwd use the same logical path, so a
// symlink such as macOS /var is not rewritten out from under the comparison.
func relativeToWorkingDir(path string) string {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	working, err := os.Getwd()
	if err != nil {
		return absolute
	}
	relative, err := filepath.Rel(working, absolute)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return absolute
	}
	if relative == "." {
		return "."
	}
	relative = filepath.ToSlash(relative)
	if !strings.HasPrefix(relative, ".") {
		relative = "./" + relative
	}
	return relative
}

func appendMetadataSources(data []byte, sources []string) []byte {
	body := strings.TrimRight(string(data), "\n")
	return []byte(body + "\n" + metadataSourcesLine(sources) + "\n")
}

func metadataSourcesLine(sources []string) string {
	if len(sources) == 0 {
		return "metadata_sources: []"
	}
	return "metadata_sources: [" + strings.Join(sources, ", ") + "]"
}

// rewriteWizardSource updates the source fields and metadata_sources of an
// existing document. Other keys stay.
func rewriteWizardSource(data []byte, fetch zsp.FetchConfig, sources []string, set bool) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse zapstore.yaml: %w", err)
	}
	root := mappingRoot(&doc)
	if root == nil {
		return nil, fmt.Errorf("parse zapstore.yaml: document is not a mapping")
	}
	applyFetchSource(root, fetch)
	if set {
		setMappingNode(root, "metadata_sources", stringListNode(sources))
	}
	out, err := yaml.Marshal(&doc)
	if err != nil {
		return nil, fmt.Errorf("write zapstore.yaml: %w", err)
	}
	return out, nil
}

func applyFetchSource(root *yaml.Node, fetch zsp.FetchConfig) {
	if fetch.Repository != "" {
		setMappingString(root, "repository", fetch.Repository)
	} else {
		removeMappingKey(root, "repository")
	}
	if fetch.ReleaseSource == nil {
		removeMappingKey(root, "release_source")
		return
	}
	if value := wizardReleaseSourceValue(fetch.ReleaseSource); value != "" {
		setMappingString(root, "release_source", value)
	}
}

func mappingRoot(doc *yaml.Node) *yaml.Node {
	if doc.Kind == yaml.DocumentNode {
		if len(doc.Content) == 0 {
			return nil
		}
		doc = doc.Content[0]
	}
	if doc.Kind != yaml.MappingNode {
		return nil
	}
	return doc
}

func setMappingString(root *yaml.Node, key, value string) {
	setMappingNode(root, key, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value})
}

func setMappingNode(root *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i < len(root.Content)-1; i += 2 {
		if root.Content[i].Value == key {
			root.Content[i+1] = value
			return
		}
	}
	root.Content = append(root.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		value,
	)
}

func removeMappingKey(root *yaml.Node, key string) {
	content := make([]*yaml.Node, 0, len(root.Content))
	for i := 0; i < len(root.Content)-1; i += 2 {
		if root.Content[i].Value == key {
			continue
		}
		content = append(content, root.Content[i], root.Content[i+1])
	}
	root.Content = content
}

func stringListNode(values []string) *yaml.Node {
	node := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle}
	for _, value := range values {
		node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value})
	}
	return node
}

func writeWizardFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if dir == "" || dir == "." {
		var err error
		dir, err = os.Getwd()
		if err != nil {
			return fmt.Errorf("write zapstore.yaml: %w", err)
		}
	}
	temp, err := os.CreateTemp(dir, ".zapstore.yaml.*")
	if err != nil {
		return fmt.Errorf("write zapstore.yaml: %w", err)
	}
	name := temp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(name)
		}
	}()
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write zapstore.yaml: %w", err)
	}
	if err := temp.Chmod(0o644); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write zapstore.yaml: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("write zapstore.yaml: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("write zapstore.yaml: %w", err)
	}
	cleanup = false
	return nil
}
