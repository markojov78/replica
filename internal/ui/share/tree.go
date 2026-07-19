package share

import (
	"path"
	"sort"
	"strings"

	"replica/internal/apiclient"
)

type treeModel struct {
	Root *folderNode
}

type folderNode struct {
	Name     string
	Path     string
	Children map[string]*folderNode
	Files    []apiclient.ReplicaInventoryFile
}

type treeFolderEntry struct {
	Name string
	Path string
}

type treeFolderView struct {
	Name     string
	Path     string
	URL      string
	IsParent bool
}

type treePanelView struct {
	Root treePanelNode
}

type treePanelNode struct {
	Name        string
	Path        string
	URL         string
	Active      bool
	HasChildren bool
	Children    []treePanelNode
}

func buildTreeModel(files []apiclient.ReplicaInventoryFile) treeModel {
	root := &folderNode{Children: make(map[string]*folderNode)}
	for _, file := range files {
		relative := cleanTreePath(file.RelativeURI)
		if relative == "" {
			continue
		}
		dir, name := path.Split(relative)
		dir = strings.TrimSuffix(dir, "/")
		if name == "" {
			continue
		}
		node := root.ensurePath(dir)
		file.RelativeURI = relative
		node.Files = append(node.Files, file)
	}
	root.sort()
	return treeModel{Root: root}
}

func (m treeModel) folder(folderPath string) *folderNode {
	folderPath = cleanTreePath(folderPath)
	if folderPath == "" {
		return m.Root
	}
	node := m.Root
	for _, part := range strings.Split(folderPath, "/") {
		if part == "" {
			continue
		}
		next := node.Children[part]
		if next == nil {
			return nil
		}
		node = next
	}
	return node
}

func (n *folderNode) ensurePath(folderPath string) *folderNode {
	node := n
	for _, part := range strings.Split(cleanTreePath(folderPath), "/") {
		if part == "" {
			continue
		}
		if node.Children == nil {
			node.Children = make(map[string]*folderNode)
		}
		child := node.Children[part]
		if child == nil {
			childPath := part
			if node.Path != "" {
				childPath = node.Path + "/" + part
			}
			child = &folderNode{Name: part, Path: childPath, Children: make(map[string]*folderNode)}
			node.Children[part] = child
		}
		node = child
	}
	return node
}

func (n *folderNode) folderEntries() []treeFolderEntry {
	children := n.sortedChildren()
	entries := make([]treeFolderEntry, 0, len(children))
	for _, child := range children {
		entries = append(entries, treeFolderEntry{Name: child.Name, Path: child.Path})
	}
	return entries
}

func (n *folderNode) sortedChildren() []*folderNode {
	children := make([]*folderNode, 0, len(n.Children))
	for _, child := range n.Children {
		children = append(children, child)
	}
	sort.SliceStable(children, func(i, j int) bool {
		return strings.ToLower(children[i].Name) < strings.ToLower(children[j].Name)
	})
	return children
}

func (n *folderNode) sort() {
	for _, child := range n.Children {
		child.sort()
	}
}

func cleanTreePath(value string) string {
	value = strings.TrimSpace(strings.Trim(value, "/"))
	if value == "" {
		return ""
	}
	cleaned := path.Clean(value)
	if cleaned == "." || cleaned == "/" || strings.HasPrefix(cleaned, "../") || cleaned == ".." {
		return ""
	}
	return strings.Trim(cleaned, "/")
}

func parentTreePath(value string) string {
	value = cleanTreePath(value)
	if value == "" {
		return ""
	}
	parent := path.Dir(value)
	if parent == "." {
		return ""
	}
	return parent
}

func treePanelFromModel(model treeModel, basePath string, viewMode string, thumbSize int, currentPath string, sortBy string, order string) treePanelView {
	currentPath = cleanTreePath(currentPath)
	root := treePanelNode{
		Name:        "/",
		URL:         browseURL(basePath, browseModeTree, viewMode, "", thumbSize, sortBy, order),
		Active:      currentPath == "",
		HasChildren: len(model.Root.Children) > 0,
		Children:    treePanelChildren(model.Root, basePath, viewMode, thumbSize, currentPath, sortBy, order),
	}
	return treePanelView{Root: root}
}

func treePanelChildren(node *folderNode, basePath string, viewMode string, thumbSize int, currentPath string, sortBy string, order string) []treePanelNode {
	children := node.sortedChildren()
	result := make([]treePanelNode, 0, len(children))
	for _, child := range children {
		panelNode := treePanelNode{
			Name:        child.Name,
			Path:        child.Path,
			URL:         browseURL(basePath, browseModeTree, viewMode, child.Path, thumbSize, sortBy, order),
			Active:      child.Path == currentPath,
			HasChildren: len(child.Children) > 0,
			Children:    treePanelChildren(child, basePath, viewMode, thumbSize, currentPath, sortBy, order),
		}
		result = append(result, panelNode)
	}
	return result
}
