package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// AddApp appends an app to the config file's apps list, preserving the
// file's comments and formatting. With a domain or a repo, a map entry
// is written; with neither, a bare path string.
func AddApp(configPath, appPath, domain, repo string) error {
	doc, err := readDocument(configPath)
	if err != nil {
		return err
	}
	root := documentRoot(doc)
	apps := mappingValue(root, "apps")
	switch {
	case apps == nil:
		apps = &yaml.Node{Kind: yaml.SequenceNode}
		root.Content = append(root.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: "apps"},
			apps,
		)
	case apps.Kind != yaml.SequenceNode:
		// An apps: key holding only comments (what init writes when it
		// finds no apps) parses as YAML null — appending to it would be
		// silently dropped. Replace it with a real list, keeping any
		// comments attached to the node.
		*apps = yaml.Node{
			Kind:        yaml.SequenceNode,
			HeadComment: apps.HeadComment,
			LineComment: apps.LineComment,
			FootComment: apps.FootComment,
		}
	}

	var entry *yaml.Node
	if domain == "" && repo == "" {
		entry = &yaml.Node{Kind: yaml.ScalarNode, Value: appPath}
	} else {
		content := []*yaml.Node{
			{Kind: yaml.ScalarNode, Value: "path"},
			{Kind: yaml.ScalarNode, Value: appPath},
		}
		if domain != "" {
			content = append(content,
				&yaml.Node{Kind: yaml.ScalarNode, Value: "domain"},
				&yaml.Node{Kind: yaml.ScalarNode, Value: domain},
			)
		}
		if repo != "" {
			content = append(content,
				&yaml.Node{Kind: yaml.ScalarNode, Value: "repo"},
				&yaml.Node{Kind: yaml.ScalarNode, Value: repo},
			)
		}
		entry = &yaml.Node{Kind: yaml.MappingNode, Content: content}
	}
	apps.Content = append(apps.Content, entry)
	return writeDocument(configPath, doc)
}

// RemoveApp deletes the app with the given resolved name from the
// config file, preserving comments. Unknown names error, listing the
// apps that do exist.
func RemoveApp(configPath, name string) error {
	doc, err := readDocument(configPath)
	if err != nil {
		return err
	}
	root := documentRoot(doc)
	apps := mappingValue(root, "apps")
	if apps == nil {
		return fmt.Errorf("app %q not found: config has no apps", name)
	}

	var known []string
	for i, entry := range apps.Content {
		entryName := appEntryName(entry)
		if entryName == name {
			apps.Content = append(apps.Content[:i], apps.Content[i+1:]...)
			return writeDocument(configPath, doc)
		}
		known = append(known, entryName)
	}
	return fmt.Errorf("app %q not found; configured apps: %s", name, strings.Join(known, ", "))
}

// appEntryName resolves the name of one apps-list node the same way
// Resolve does: explicit name, else the slugified path basename.
func appEntryName(entry *yaml.Node) string {
	if entry.Kind == yaml.ScalarNode {
		return Slugify(filepath.Base(entry.Value))
	}
	if entry.Kind == yaml.MappingNode {
		if n := mappingValue(entry, "name"); n != nil {
			return n.Value
		}
		if p := mappingValue(entry, "path"); p != nil {
			return Slugify(filepath.Base(p.Value))
		}
	}
	return ""
}

// readDocument parses the config file into a comment-preserving node
// tree.
func readDocument(path string) (*yaml.Node, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	if doc.Kind == 0 || len(doc.Content) == 0 {
		// Empty file: synthesize an empty mapping document.
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{
			{Kind: yaml.MappingNode},
		}}
	}
	return &doc, nil
}

func documentRoot(doc *yaml.Node) *yaml.Node {
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		return doc.Content[0]
	}
	return doc
}

// mappingValue returns the value node for a key in a mapping, or nil.
func mappingValue(mapping *yaml.Node, key string) *yaml.Node {
	if mapping.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

func writeDocument(path string, doc *yaml.Node) error {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		return fmt.Errorf("write config %s: %w", path, err)
	}
	return nil
}
