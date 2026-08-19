package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestSearchTUISelectsAndMovesResults(t *testing.T) {
	model := searchTUIModel{
		results: []SearchResult{
			{Name: "first", Source: "acme/first", Installs: 2},
			{Name: "second", Source: "acme/second", Installs: 1},
		},
		height: 24,
		width:  80,
	}

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyDown})
	model = updated.(searchTUIModel)
	result, ok := model.selected()
	if !ok || result.Name != "second" {
		t.Fatalf("selected = %+v, %v; want second, true", result, ok)
	}
	if !strings.Contains(model.View(), "enter install") {
		t.Fatal("search TUI does not show the install shortcut")
	}
}

func TestSearchTUIOffersUpdateForInstalledSkill(t *testing.T) {
	model := searchTUIModel{
		results: []SearchResult{
			{Name: "demo", Source: "acme/tools/skills/demo", installedID: "acme/tools:skills/demo"},
		},
		height: 24,
		width:  80,
	}

	view := model.View()
	if !strings.Contains(view, "installed") {
		t.Fatal("search TUI does not mark installed skill")
	}
	if !strings.Contains(view, "enter update") {
		t.Fatal("search TUI does not offer update for installed skill")
	}
}

func TestInstallSearchResultRejectsIncompleteResult(t *testing.T) {
	err := installSearchResult(t.Context(), nil, t.TempDir(), SearchResult{Name: "demo"})
	if err == nil || !strings.Contains(err.Error(), "source") {
		t.Fatalf("error = %v, want missing source error", err)
	}
}
