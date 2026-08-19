package main

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

type tuiModel struct {
	ctx     context.Context
	lib     *Library
	project string
	skills  []Skill
	active  map[string]bool
	cursor  int
	offset  int
	height  int
	width   int
	busy    bool
	status  string
}

type operationDone struct {
	status string
	err    error
}

func runTUI(ctx context.Context, lib *Library, project string) error {
	active, err := lib.Active(ctx, project)
	if err != nil {
		return err
	}
	skills, err := lib.List(ctx)
	if err != nil {
		return err
	}
	model := tuiModel{
		ctx:     ctx,
		lib:     lib,
		project: project,
		skills:  skills,
		active:  active,
		height:  24,
		width:   80,
	}
	_, err = tea.NewProgram(model, tea.WithAltScreen()).Run()
	return err
}

type searchTUIModel struct {
	ctx     context.Context
	lib     *Library
	project string
	query   string
	results []SearchResult
	cursor  int
	offset  int
	height  int
	width   int
	busy    bool
	status  string
}

type searchOperationDone struct {
	status string
	err    error
}

func runSearchTUI(ctx context.Context, lib *Library, project, query string, results []SearchResult) error {
	model := searchTUIModel{
		ctx:     ctx,
		lib:     lib,
		project: project,
		query:   query,
		results: results,
		height:  24,
		width:   80,
	}
	_, err := tea.NewProgram(model, tea.WithAltScreen()).Run()
	return err
}

func (m searchTUIModel) Init() tea.Cmd {
	return nil
}

func (m searchTUIModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = message.Width, message.Height
		m.clamp()
	case tea.KeyMsg:
		if m.busy {
			if message.String() == "ctrl+c" {
				return m, tea.Quit
			}
			return m, nil
		}
		switch message.String() {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
			m.clamp()
		case "down", "j":
			if m.cursor+1 < len(m.results) {
				m.cursor++
			}
			m.clamp()
		case "enter", " ":
			if result, ok := m.selected(); ok {
				m.busy = true
				action := "installing "
				status := "installed "
				operation := func() error {
					return installSearchResult(m.ctx, m.lib, m.project, result)
				}
				if result.installedID != "" {
					action = "updating "
					status = "updated "
					operation = func() error {
						_, err := m.lib.Update(m.ctx, result.installedID)
						return err
					}
				}
				m.status = action + result.Name + "…"
				return m, func() tea.Msg {
					err := operation()
					return searchOperationDone{status: status + result.Name, err: err}
				}
			}
		}
	case searchOperationDone:
		m.busy = false
		if message.err != nil {
			m.status = "error: " + message.err.Error()
		} else {
			m.status = message.status
		}
	}
	return m, nil
}

func (m searchTUIModel) View() string {
	var out strings.Builder
	out.WriteString("\x1b[1m skillman find\x1b[0m  ")
	out.WriteString("\x1b[2m")
	out.WriteString(m.query)
	out.WriteString("\x1b[0m\n\n")

	end := min(len(m.results), m.offset+m.listHeight())
	for i := m.offset; i < end; i++ {
		result := m.results[i]
		cursor := "  "
		if i == m.cursor {
			cursor = "\x1b[36m›\x1b[0m "
		}
		state := ""
		if result.installedID != "" {
			state = "  installed"
		}
		line := fmt.Sprintf("%s%-28s \x1b[2m%s  %d installs%s\x1b[0m", cursor, result.Name, result.Source, result.Installs, state)
		out.WriteString(truncateANSI(line, m.width))
		out.WriteByte('\n')
	}

	for lines := strings.Count(out.String(), "\n"); lines < m.height-2; lines++ {
		out.WriteByte('\n')
	}
	if m.status != "" {
		out.WriteString(truncate(m.status, m.width))
		out.WriteByte('\n')
	} else {
		out.WriteByte('\n')
	}
	action := "install"
	if result, ok := m.selected(); ok && result.installedID != "" {
		action = "update"
	}
	out.WriteString("\x1b[2m↑/↓ move  enter ")
	out.WriteString(action)
	out.WriteString("  q quit\x1b[0m")
	return out.String()
}

func (m *searchTUIModel) clamp() {
	if len(m.results) == 0 {
		m.cursor, m.offset = 0, 0
		return
	}
	if m.cursor >= len(m.results) {
		m.cursor = len(m.results) - 1
	}
	height := m.listHeight()
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+height {
		m.offset = m.cursor - height + 1
	}
}

func (m searchTUIModel) listHeight() int {
	return max(1, m.height-5)
}

func (m searchTUIModel) selected() (SearchResult, bool) {
	if m.cursor < 0 || m.cursor >= len(m.results) {
		return SearchResult{}, false
	}
	return m.results[m.cursor], true
}

func installSearchResult(ctx context.Context, lib *Library, project string, result SearchResult) error {
	if result.Source == "" || result.Name == "" {
		return fmt.Errorf("search result is missing a source or skill name")
	}
	skills, err := lib.Pull(ctx, result.Source, []string{result.Name})
	if err != nil {
		return fmt.Errorf("pull %s: %w", result.Name, err)
	}
	for _, skill := range skills {
		if err := lib.Activate(ctx, project, skill.ID); err != nil {
			return fmt.Errorf("add %s: %w", skill.Name, err)
		}
	}
	return nil
}

func (m tuiModel) Init() tea.Cmd {
	return nil
}

func (m tuiModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = message.Width, message.Height
		m.clamp()
	case tea.KeyMsg:
		if m.busy {
			if message.String() == "ctrl+c" {
				return m, tea.Quit
			}
			return m, nil
		}
		switch message.String() {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
			m.clamp()
		case "down", "j":
			if m.cursor+1 < len(m.skills) {
				m.cursor++
			}
			m.clamp()
		case " ":
			if skill, ok := m.selected(); ok {
				m.busy = true
				m.status = "working…"
				active := m.active[skill.ID]
				return m, func() tea.Msg {
					var err error
					status := "added " + skill.Name
					if active {
						err = m.lib.Deactivate(m.ctx, m.project, skill.ID)
						status = "removed " + skill.Name
					} else {
						err = m.lib.Activate(m.ctx, m.project, skill.ID)
					}
					return operationDone{status: status, err: err}
				}
			}
		case "u":
			if skill, ok := m.selected(); ok {
				m.busy = true
				m.status = "updating " + skill.Name + "…"
				return m, func() tea.Msg {
					_, err := m.lib.Update(m.ctx, skill.ID)
					return operationDone{status: "updated " + skill.Name, err: err}
				}
			}
		case "s":
			m.busy = true
			m.status = "syncing active skills…"
			return m, func() tea.Msg {
				err := m.lib.Sync(m.ctx, m.project)
				return operationDone{status: "synced active skills", err: err}
			}
		}
	case operationDone:
		m.busy = false
		if message.err != nil {
			m.status = "error: " + message.err.Error()
		} else {
			m.status = message.status
		}
		skills, err := m.lib.List(m.ctx)
		if err != nil {
			m.status = "error: " + err.Error()
		} else {
			m.skills = skills
		}
		active, err := m.lib.Active(m.ctx, m.project)
		if err != nil {
			m.status = "error: " + err.Error()
		} else {
			m.active = active
		}
		m.clamp()
	}
	return m, nil
}

func (m tuiModel) View() string {
	var out strings.Builder
	out.WriteString("\x1b[1m skillman\x1b[0m  ")
	out.WriteString("\x1b[2m")
	out.WriteString(m.project)
	out.WriteString("\x1b[0m\n\n")

	if len(m.skills) == 0 {
		out.WriteString(" No skills yet.\n")
		out.WriteString(" Run \x1b[1mskillman pull owner/repo\x1b[0m or \x1b[1mskillman link ./path\x1b[0m.\n")
	} else {
		end := min(len(m.skills), m.offset+m.listHeight())
		duplicateNames := duplicates(m.skills)
		for i := m.offset; i < end; i++ {
			skill := m.skills[i]
			cursor := "  "
			if i == m.cursor {
				cursor = "\x1b[36m›\x1b[0m "
			}
			marker := "○"
			if m.active[skill.ID] {
				marker = "\x1b[32m●\x1b[0m"
			}
			source := skill.Source
			if skill.Linked {
				source += " (linked)"
			}
			if !duplicateNames[skill.Name] && m.width < 64 {
				source = ""
			}
			line := fmt.Sprintf("%s%s %-24s \x1b[2m%s\x1b[0m", cursor, marker, skill.Name, source)
			out.WriteString(truncateANSI(line, m.width))
			out.WriteByte('\n')
		}
	}

	for lines := strings.Count(out.String(), "\n"); lines < m.height-2; lines++ {
		out.WriteByte('\n')
	}
	if m.status != "" {
		out.WriteString(truncate(m.status, m.width))
		out.WriteByte('\n')
	} else {
		out.WriteByte('\n')
	}
	out.WriteString("\x1b[2m↑/↓ move  space toggle  u update  s sync  q quit\x1b[0m")
	return out.String()
}

func (m *tuiModel) clamp() {
	if len(m.skills) == 0 {
		m.cursor, m.offset = 0, 0
		return
	}
	if m.cursor >= len(m.skills) {
		m.cursor = len(m.skills) - 1
	}
	height := m.listHeight()
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+height {
		m.offset = m.cursor - height + 1
	}
}

func (m tuiModel) listHeight() int {
	return max(1, m.height-5)
}

func (m tuiModel) selected() (Skill, bool) {
	if m.cursor < 0 || m.cursor >= len(m.skills) {
		return Skill{}, false
	}
	return m.skills[m.cursor], true
}

func duplicates(skills []Skill) map[string]bool {
	counts := make(map[string]int)
	for _, skill := range skills {
		counts[skill.Name]++
	}
	result := make(map[string]bool)
	for name, count := range counts {
		result[name] = count > 1
	}
	return result
}

func truncate(value string, width int) string {
	if width < 2 || len([]rune(value)) <= width {
		return value
	}
	return string([]rune(value)[:width-1]) + "…"
}

// truncateANSI only accounts for the fixed color escapes emitted above.
func truncateANSI(value string, width int) string {
	plain := value
	for _, escape := range []string{"\x1b[36m", "\x1b[32m", "\x1b[2m", "\x1b[0m"} {
		plain = strings.ReplaceAll(plain, escape, "")
	}
	if len([]rune(plain)) <= width {
		return value
	}
	return truncate(plain, width)
}
