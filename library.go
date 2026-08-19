package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

type Skill struct {
	ID       string
	Name     string
	Source   string
	RepoURL  string
	RepoPath string
	Commit   string
	Linked   bool
	dir      string
}

type Library struct {
	home string
}

const repoCacheDir = ".repos"
const skillsSearchURL = "https://skills.sh/api/search"

type SearchResult struct {
	Name        string
	SkillID     string
	Source      string
	Installs    int
	installedID string
}

func OpenLibrary() (*Library, error) {
	home := os.Getenv("SKILLMAN_HOME")
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("find home directory: %w", err)
		}
		home = filepath.Join(userHome, ".skillman")
	}
	if err := os.MkdirAll(filepath.Join(home, "skills"), 0o755); err != nil {
		return nil, fmt.Errorf("create library: %w", err)
	}

	return &Library{home: home}, nil
}

func (l *Library) List(ctx context.Context) ([]Skill, error) {
	var skills []Skill
	local, err := l.listLocal()
	if err != nil {
		return nil, err
	}
	repos, err := l.listRepos(ctx)
	if err != nil {
		return nil, err
	}
	skills = append(local, repos...)
	sort.Slice(skills, func(i, j int) bool {
		if skills[i].Name == skills[j].Name {
			return skills[i].Source < skills[j].Source
		}
		return skills[i].Name < skills[j].Name
	})
	return skills, nil
}

func (l *Library) Find(ctx context.Context, query string) ([]SearchResult, error) {
	results, err := searchSkills(ctx, skillsSearchURL, query, 5)
	if err != nil {
		return nil, err
	}
	skills, err := l.List(ctx)
	if err != nil {
		return nil, err
	}
	return markInstalledSearchResults(results, skills), nil
}

func searchSkills(ctx context.Context, endpoint, query string, limit int) ([]SearchResult, error) {
	if strings.TrimSpace(query) == "" {
		return nil, errors.New("search query is required")
	}
	requestURL, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("parse skills search URL: %w", err)
	}
	params := requestURL.Query()
	params.Set("q", query)
	params.Set("limit", fmt.Sprint(limit))
	requestURL.RawQuery = params.Encode()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create skills search request: %w", err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("search skills: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("search skills: %s", response.Status)
	}
	var payload struct {
		Skills []SearchResult `json:"skills"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode skills search response: %w", err)
	}
	if len(payload.Skills) > limit {
		payload.Skills = payload.Skills[:limit]
	}
	return payload.Skills, nil
}

func markInstalledSearchResults(results []SearchResult, skills []Skill) []SearchResult {
	for i := range results {
		repoURL, _, _, err := parseSource(results[i].Source)
		if err != nil {
			continue
		}
		for _, skill := range skills {
			if skill.Name == results[i].Name && skill.RepoURL == repoURL {
				results[i].installedID = skill.ID
				break
			}
		}
	}
	return results
}

func (l *Library) Pull(ctx context.Context, source string, names []string) ([]Skill, error) {
	repoURL, subpath, label, err := parseSource(source)
	if err != nil {
		return nil, err
	}
	cache := filepath.Join(l.home, repoCacheDir, shortHash(repoURL))
	if err := ensureRepo(ctx, cache, repoURL); err != nil {
		return nil, err
	}

	if err := checkoutSkillManifests(ctx, cache, subpath); err != nil {
		return nil, err
	}
	root := filepath.Join(cache, filepath.FromSlash(subpath))
	paths, err := discoverSkills(root)
	if err != nil {
		return nil, fmt.Errorf("discover skills: %w", err)
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("no SKILL.md found under %s", source)
	}
	commit, err := gitOutput(ctx, cache, "rev-parse", "HEAD")
	if err != nil {
		return nil, err
	}

	var discovered []Skill
	for _, path := range paths {
		rel, err := filepath.Rel(cache, path)
		if err != nil {
			return nil, fmt.Errorf("resolve skill path: %w", err)
		}
		rel = filepath.ToSlash(rel)
		name, err := skillName(path)
		if err != nil {
			return nil, err
		}
		discovered = append(discovered, Skill{
			ID:       label + ":" + rel,
			Name:     name,
			Source:   label + "/" + rel,
			RepoURL:  repoURL,
			RepoPath: rel,
			Commit:   commit,
			dir:      path,
		})
	}
	installed, err := selectPulledSkills(preferCanonicalSkills(discovered), names)
	if err != nil {
		return nil, err
	}
	previous, _, err := readRepoPaths(cache)
	if err != nil {
		return nil, err
	}
	paths = append(previous, skillRepoPaths(installed)...)
	if err := checkoutSkillDirectories(ctx, cache, paths); err != nil {
		return nil, err
	}
	if err := writeRepoPaths(cache, paths); err != nil {
		return nil, err
	}
	return installed, nil
}

func (l *Library) Link(path string) (Skill, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return Skill{}, fmt.Errorf("resolve link path: %w", err)
	}
	absolute, err = filepath.EvalSymlinks(absolute)
	if err != nil {
		return Skill{}, fmt.Errorf("resolve link target: %w", err)
	}
	name, err := skillName(absolute)
	if err != nil {
		return Skill{}, err
	}
	skill := Skill{
		ID:     "local:" + absolute,
		Name:   name,
		Source: absolute,
		Linked: true,
	}
	skill.dir = l.skillPath(skill)
	if err := replaceWithSymlink(absolute, skill.dir); err != nil {
		return Skill{}, fmt.Errorf("link skill: %w", err)
	}
	return skill, nil
}

func (l *Library) Update(ctx context.Context, selector string) ([]Skill, error) {
	selected, err := l.selectSkills(ctx, selector)
	if err != nil {
		return nil, err
	}
	repos := make(map[string]string)
	for _, skill := range selected {
		if !skill.Linked && skill.RepoURL != "" {
			repos[skill.RepoURL] = filepath.Join(l.home, repoCacheDir, shortHash(skill.RepoURL))
		}
	}
	for _, cache := range repos {
		if err := updateRepo(ctx, cache); err != nil {
			return nil, err
		}
	}

	var updated []Skill
	for _, skill := range selected {
		if skill.Linked || skill.RepoURL == "" {
			updated = append(updated, skill)
			continue
		}
		cache := repos[skill.RepoURL]
		path := filepath.Join(cache, filepath.FromSlash(skill.RepoPath))
		name, err := skillName(path)
		if err != nil {
			return nil, fmt.Errorf("update %s: %w", skill.Source, err)
		}
		commit, err := gitOutput(ctx, cache, "rev-parse", "HEAD")
		if err != nil {
			return nil, err
		}
		skill.Name, skill.Commit, skill.dir = name, commit, path
		updated = append(updated, skill)
	}
	return updated, nil
}

func (l *Library) Activate(ctx context.Context, project, selector string) error {
	skill, err := l.resolve(ctx, selector)
	if err != nil {
		return err
	}
	if err := replaceWithCopy(l.skillPath(skill), skillDestination(project, skill)); err != nil {
		return fmt.Errorf("activate %s: %w", skill.Name, err)
	}
	return removeProjectState(project)
}

func (l *Library) Deactivate(ctx context.Context, project, selector string) error {
	skill, err := l.resolve(ctx, selector)
	name := selector
	dest, destErr := skillDestinationByName(project, selector)
	if err == nil {
		name = skill.Name
		dest = skillDestination(project, skill)
		destErr = nil
	}
	if destErr != nil {
		return destErr
	}
	if _, err := os.Lstat(dest); errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%s is not active", name)
	} else if err != nil {
		return fmt.Errorf("inspect active skill: %w", err)
	}
	if err := os.RemoveAll(dest); err != nil {
		return fmt.Errorf("remove active skill: %w", err)
	}
	return removeProjectState(project)
}

func (l *Library) Sync(ctx context.Context, project string) error {
	skills, err := l.List(ctx)
	if err != nil {
		return err
	}
	seen := make(map[string]bool)
	for _, skill := range skills {
		dest := skillDestination(project, skill)
		if seen[dest] {
			continue
		}
		seen[dest] = true
		if _, err := os.Lstat(dest); err == nil {
			if err := replaceWithCopy(l.skillPath(skill), dest); err != nil {
				return fmt.Errorf("sync %s: %w", skill.Name, err)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect active skill: %w", err)
		}
	}
	return removeProjectState(project)
}

func (l *Library) Active(ctx context.Context, project string) (map[string]bool, error) {
	skills, err := l.List(ctx)
	if err != nil {
		return nil, err
	}
	active := make(map[string]bool)
	for _, skill := range skills {
		if _, err := os.Lstat(skillDestination(project, skill)); err == nil {
			active[skill.ID] = true
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("inspect active skill: %w", err)
		}
	}
	return active, nil
}

func skillDestination(project string, skill Skill) string {
	return filepath.Join(project, ".agents", "skills", slug(skill.Name))
}

func skillDestinationByName(project, name string) (string, error) {
	if name == "" || filepath.Base(name) != name || name == "." {
		return "", fmt.Errorf("invalid skill directory name %q", name)
	}
	return filepath.Join(project, ".agents", "skills", name), nil
}

func (l *Library) resolve(ctx context.Context, selector string) (Skill, error) {
	skills, err := l.List(ctx)
	if err != nil {
		return Skill{}, err
	}
	return resolveSkill(skills, selector)
}

func (l *Library) selectSkills(ctx context.Context, selector string) ([]Skill, error) {
	skills, err := l.List(ctx)
	if err != nil {
		return nil, err
	}
	if selector == "" {
		return skills, nil
	}
	skill, err := resolveSkill(skills, selector)
	if err != nil {
		return nil, err
	}
	return []Skill{skill}, nil
}

func resolveSkill(skills []Skill, selector string) (Skill, error) {
	var matches []Skill
	for _, skill := range skills {
		if skill.ID == selector || skill.Source == selector || skill.Name == selector {
			matches = append(matches, skill)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) == 0 {
		return Skill{}, fmt.Errorf("skill %q not found", selector)
	}
	sources := make([]string, len(matches))
	for i, skill := range matches {
		sources[i] = skill.Source
	}
	sort.Strings(sources)
	return Skill{}, fmt.Errorf("skill %q is ambiguous; use one of: %s", selector, strings.Join(sources, ", "))
}

func (l *Library) skillPath(skill Skill) string {
	if skill.dir != "" {
		return skill.dir
	}
	if skill.Linked {
		return filepath.Join(l.home, "skills", slug(skill.Name)+"--"+shortHash(skill.ID))
	}
	return filepath.Join(l.home, repoCacheDir, shortHash(skill.RepoURL), filepath.FromSlash(skill.RepoPath))
}

func (l *Library) listLocal() ([]Skill, error) {
	var skills []Skill
	repos := filepath.Join(l.home, repoCacheDir)
	err := filepath.WalkDir(l.home, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			if os.IsPermission(err) || errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if path == repos {
			return filepath.SkipDir
		}
		if entry.IsDir() && entry.Name() == ".git" {
			return filepath.SkipDir
		}
		if entry.Name() == "SKILL.md" {
			dir := filepath.Dir(path)
			if dir == l.home {
				return nil
			}
			name, err := skillName(dir)
			if err != nil {
				return nil
			}
			skills = append(skills, Skill{
				ID:     "local:" + dir,
				Name:   name,
				Source: dir,
				dir:    dir,
			})
			return filepath.SkipDir
		}
		if entry.Type()&fs.ModeSymlink == 0 {
			return nil
		}
		absolute, err := filepath.EvalSymlinks(path)
		if err != nil {
			return nil
		}
		info, err := os.Stat(absolute)
		if err != nil {
			return nil
		}
		if !info.IsDir() {
			return nil
		}
		name, err := skillName(absolute)
		if err != nil {
			return nil
		}
		skills = append(skills, Skill{
			ID:     "local:" + absolute,
			Name:   name,
			Source: absolute,
			Linked: true,
			dir:    path,
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("discover library skills: %w", err)
	}
	return skills, nil
}

func (l *Library) listRepos(ctx context.Context) ([]Skill, error) {
	root := filepath.Join(l.home, repoCacheDir)
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read repository cache: %w", err)
	}
	var skills []Skill
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if strings.HasSuffix(entry.Name(), ".partial") {
			continue
		}
		cache := filepath.Join(root, entry.Name())
		if _, err := os.Stat(filepath.Join(cache, ".git")); err != nil {
			continue
		}
		repoURL, err := gitOutput(ctx, cache, "remote", "get-url", "origin")
		if err != nil {
			continue
		}
		_, _, label, err := parseSource(repoURL)
		if err != nil {
			continue
		}
		commit, err := gitOutput(ctx, cache, "rev-parse", "HEAD")
		if err != nil {
			continue
		}
		roots, err := repoSkillRoots(cache)
		if err != nil {
			return nil, err
		}
		seen := make(map[string]bool)
		var paths []string
		for _, root := range roots {
			found, err := discoverSkills(root)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("discover skills in %s: %w", cache, err)
			}
			for _, path := range found {
				if seen[path] {
					continue
				}
				seen[path] = true
				paths = append(paths, path)
			}
		}
		var repoSkills []Skill
		for _, path := range paths {
			rel, err := filepath.Rel(cache, path)
			if err != nil {
				return nil, fmt.Errorf("resolve skill path: %w", err)
			}
			rel = filepath.ToSlash(rel)
			name, err := skillName(path)
			if err != nil {
				return nil, err
			}
			repoSkills = append(repoSkills, Skill{
				ID:       label + ":" + rel,
				Name:     name,
				Source:   label + "/" + rel,
				RepoURL:  repoURL,
				RepoPath: rel,
				Commit:   commit,
				dir:      path,
			})
		}
		skills = append(skills, preferCanonicalSkills(repoSkills)...)
	}
	return skills, nil
}

func parseSource(source string) (repoURL, subpath, label string, err error) {
	source = strings.TrimSuffix(strings.TrimSpace(source), "/")
	if source == "" {
		return "", "", "", errors.New("source is required")
	}
	if strings.Contains(source, "://") || strings.HasPrefix(source, "git@") {
		return parseGitURL(source)
	}
	parts := strings.Split(source, "/")
	if len(parts) < 2 {
		return "", "", "", fmt.Errorf("source must be owner/repo or a git URL")
	}
	label = parts[0] + "/" + strings.TrimSuffix(parts[1], ".git")
	repoURL = "https://github.com/" + label + ".git"
	if len(parts) > 2 {
		subpath = strings.Join(parts[2:], "/")
	}
	return repoURL, subpath, label, nil
}

func parseGitURL(source string) (repoURL, subpath, label string, err error) {
	host, rest, ssh, err := splitGitURL(source)
	if err != nil {
		return "", "", "", err
	}
	repoPath, subpath := splitRepoAndSkillPath(rest)
	if strings.Count(repoPath, "/") < 1 || strings.HasPrefix(repoPath, "/") {
		return "", "", "", fmt.Errorf("invalid git source %q", source)
	}
	if host == "github.com" {
		label = repoPath
		repoURL = "https://github.com/" + repoPath + ".git"
		return repoURL, subpath, label, nil
	}
	label = host + "/" + repoPath
	if ssh {
		repoURL = "git@" + host + ":" + repoPath + ".git"
	} else {
		repoURL = "https://" + host + "/" + repoPath + ".git"
	}
	return repoURL, subpath, label, nil
}

func splitGitURL(source string) (host, rest string, ssh bool, err error) {
	if strings.HasPrefix(source, "git@") {
		host, rest, ok := strings.Cut(strings.TrimPrefix(source, "git@"), ":")
		if !ok || host == "" || rest == "" {
			return "", "", false, fmt.Errorf("invalid git source %q", source)
		}
		return host, rest, true, nil
	}
	parsed, err := url.Parse(source)
	if err != nil || parsed.Host == "" {
		return "", "", false, fmt.Errorf("invalid git source %q", source)
	}
	return parsed.Host, strings.TrimPrefix(parsed.Path, "/"), parsed.Scheme == "ssh", nil
}

func splitRepoAndSkillPath(rest string) (repoPath, subpath string) {
	rest = strings.Trim(rest, "/")
	if i := strings.Index(rest, ".git"); i >= 0 {
		return rest[:i], stripForgeBrowsePath(strings.TrimPrefix(rest[i+len(".git"):], "/"))
	}
	parts := strings.Split(rest, "/")
	if len(parts) < 2 {
		return rest, ""
	}
	return parts[0] + "/" + parts[1], stripForgeBrowsePath(strings.Join(parts[2:], "/"))
}

func stripForgeBrowsePath(path string) string {
	parts := strings.Split(path, "/")
	switch {
	case len(parts) >= 2 && (parts[0] == "tree" || parts[0] == "blob"):
		return strings.Join(parts[2:], "/")
	case len(parts) >= 3 && parts[0] == "src" && parts[1] == "branch":
		return strings.Join(parts[3:], "/")
	default:
		return path
	}
}

func preferCanonicalSkills(skills []Skill) []Skill {
	type key struct{ repo, name string }
	winner := make(map[key]Skill)
	var order []key
	for _, skill := range skills {
		k := key{skill.RepoURL, skill.Name}
		prev, ok := winner[k]
		if !ok {
			winner[k] = skill
			order = append(order, k)
			continue
		}
		if betterSkillPath(skill.RepoPath, prev.RepoPath) {
			winner[k] = skill
		}
	}
	out := make([]Skill, 0, len(order))
	for _, k := range order {
		out = append(out, winner[k])
	}
	return out
}

func betterSkillPath(a, b string) bool {
	ra, rb := skillPathRank(a), skillPathRank(b)
	if ra != rb {
		return ra < rb
	}
	if strings.Count(a, "/") != strings.Count(b, "/") {
		return strings.Count(a, "/") < strings.Count(b, "/")
	}
	return a < b
}

func skillPathRank(rel string) int {
	if rel == "" || rel == "." {
		return 1
	}
	first, _, _ := strings.Cut(rel, "/")
	switch {
	case first == "skills":
		return 0
	case first == "plugins" || strings.HasPrefix(first, "."):
		return 3
	default:
		return 2
	}
}

func wantAllSkills(names []string) bool {
	if len(names) == 0 {
		return true
	}
	for _, name := range names {
		if name == "*" {
			return true
		}
	}
	return false
}

func selectPulledSkills(skills []Skill, names []string) ([]Skill, error) {
	if wantAllSkills(names) {
		return skills, nil
	}
	wanted := make(map[string]bool, len(names))
	for _, name := range names {
		wanted[name] = true
	}
	var selected []Skill
	found := make(map[string]bool)
	for _, skill := range skills {
		if wanted[skill.Name] {
			selected = append(selected, skill)
			found[skill.Name] = true
		}
	}
	var missing []string
	for _, name := range names {
		if name != "*" && !found[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		available := make([]string, len(skills))
		for i, skill := range skills {
			available[i] = skill.Name
		}
		sort.Strings(available)
		return nil, fmt.Errorf("skill %s not found; available: %s", strings.Join(missing, ", "), strings.Join(available, ", "))
	}
	return selected, nil
}

func repoPathsFile(cache string) string {
	return cache + ".paths"
}

func writeRepoPaths(cache string, paths []string) error {
	paths = append([]string(nil), paths...)
	sort.Strings(paths)
	paths = compactStrings(paths)
	return writeFileAtomic(repoPathsFile(cache), []byte(strings.Join(paths, "\n")))
}

func rememberRepoPath(cache, subpath string) error {
	file := repoPathsFile(cache)
	if subpath == "" {
		return os.RemoveAll(file)
	}
	paths, restricted, err := readRepoPaths(cache)
	if err != nil {
		return err
	}
	if !restricted {
		return writeFileAtomic(file, []byte(subpath))
	}
	for _, path := range paths {
		if path == subpath {
			return nil
		}
	}
	paths = append(paths, subpath)
	return writeRepoPaths(cache, paths)
}

func readRepoPaths(cache string) ([]string, bool, error) {
	data, err := os.ReadFile(repoPathsFile(cache))
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read repository paths: %w", err)
	}
	var paths []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			paths = append(paths, line)
		}
	}
	return paths, true, nil
}

func repoSkillRoots(cache string) ([]string, error) {
	paths, restricted, err := readRepoPaths(cache)
	if err != nil {
		return nil, err
	}
	if !restricted {
		return []string{cache}, nil
	}
	roots := make([]string, 0, len(paths))
	for _, path := range paths {
		roots = append(roots, filepath.Join(cache, filepath.FromSlash(path)))
	}
	return roots, nil
}

func checkoutSkillManifests(ctx context.Context, cache, subpath string) error {
	prefix := strings.Trim(filepath.ToSlash(filepath.Clean(subpath)), "/")
	patterns := []string{"/SKILL.md", "/**/SKILL.md"}
	if prefix != "" && prefix != "." {
		patterns = []string{
			"/" + prefix + "/SKILL.md",
			"/" + prefix + "/**/SKILL.md",
		}
	}
	return setSparseCheckout(ctx, cache, patterns)
}

func checkoutSkillDirectories(ctx context.Context, cache string, paths []string) error {
	patterns := make([]string, 0, len(paths)*2)
	for _, path := range compactStrings(append([]string(nil), paths...)) {
		path = strings.Trim(filepath.ToSlash(filepath.Clean(path)), "/")
		if path == "" || path == "." {
			patterns = append(patterns, "/*")
			continue
		}
		patterns = append(patterns, "/"+path+"/*", "/"+path+"/**")
	}
	if len(patterns) == 0 {
		return errors.New("no skill directories to check out")
	}
	return setSparseCheckout(ctx, cache, patterns)
}

func setSparseCheckout(ctx context.Context, cache string, patterns []string) error {
	args := append([]string{"sparse-checkout", "set", "--no-cone", "--sparse-index"}, patterns...)
	if err := runGit(ctx, cache, args...); err != nil {
		return fmt.Errorf("sparse-checkout skill files: %w", err)
	}
	// clone --no-checkout leaves the work tree empty; sparse-checkout set
	// only records patterns until a checkout materializes matching files.
	if err := runGit(ctx, cache, "checkout", "HEAD"); err != nil {
		return fmt.Errorf("checkout skill files: %w", err)
	}
	return nil
}

func skillRepoPaths(skills []Skill) []string {
	paths := make([]string, 0, len(skills))
	for _, skill := range skills {
		paths = append(paths, skill.RepoPath)
	}
	return compactStrings(paths)
}

func compactStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	sort.Strings(values)
	out := values[:0]
	for _, value := range values {
		if len(out) == 0 || out[len(out)-1] != value {
			out = append(out, value)
		}
	}
	return out
}

func discoverSkills(root string) ([]string, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", root)
	}
	var paths []string
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() && entry.Name() == ".git" {
			return filepath.SkipDir
		}
		if entry.Name() == "SKILL.md" {
			paths = append(paths, filepath.Dir(path))
			return filepath.SkipDir
		}
		return nil
	})
	sort.Strings(paths)
	return paths, err
}

func skillName(dir string) (string, error) {
	file, err := os.Open(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		return "", fmt.Errorf("open %s/SKILL.md: %w", dir, err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(io.LimitReader(file, 64<<10))
	inFrontmatter := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "---" {
			if !inFrontmatter {
				inFrontmatter = true
				continue
			}
			break
		}
		if inFrontmatter {
			if value, ok := strings.CutPrefix(line, "name:"); ok {
				name := strings.Trim(strings.TrimSpace(value), `"'`)
				if name != "" {
					return name, nil
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read SKILL.md: %w", err)
	}
	name := filepath.Base(dir)
	if name == "." || name == string(filepath.Separator) {
		return "", fmt.Errorf("%s/SKILL.md has no name", dir)
	}
	return name, nil
}

func replaceWithSymlink(source, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	if err := os.RemoveAll(dest); err != nil {
		return err
	}
	return os.Symlink(source, dest)
}

func replaceWithCopy(source, dest string) error {
	resolved, err := filepath.EvalSymlinks(source)
	if err != nil {
		return err
	}
	temp := dest + ".tmp-" + shortHash(resolved)
	backup := dest + ".old"
	if err := os.RemoveAll(temp); err != nil {
		return err
	}
	if err := copyDir(resolved, temp); err != nil {
		os.RemoveAll(temp)
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		os.RemoveAll(temp)
		return err
	}
	if err := os.RemoveAll(backup); err != nil {
		os.RemoveAll(temp)
		return err
	}
	hadDest := false
	if _, err := os.Lstat(dest); err == nil {
		if err := os.Rename(dest, backup); err != nil {
			os.RemoveAll(temp)
			return err
		}
		hadDest = true
	} else if !errors.Is(err, os.ErrNotExist) {
		os.RemoveAll(temp)
		return err
	}
	if err := os.Rename(temp, dest); err != nil {
		if hadDest {
			_ = os.Rename(backup, dest)
		}
		return err
	}
	if hadDest {
		return os.RemoveAll(backup)
	}
	return nil
}

func copyDir(source, dest string) error {
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", source)
	}
	if err := os.MkdirAll(dest, info.Mode().Perm()); err != nil {
		return err
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		src := filepath.Join(source, entry.Name())
		dst := filepath.Join(dest, entry.Name())
		entryInfo, err := os.Stat(src)
		if err != nil {
			return err
		}
		if entryInfo.IsDir() {
			if err := copyDir(src, dst); err != nil {
				return err
			}
			continue
		}
		if !entryInfo.Mode().IsRegular() {
			return fmt.Errorf("unsupported file %s", src)
		}
		if err := copyFile(src, dst, entryInfo.Mode().Perm()); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(source, dest string, mode fs.FileMode) error {
	src, err := os.Open(source)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		return err
	}
	return dst.Close()
}

func removeProjectState(project string) error {
	err := os.Remove(filepath.Join(project, ".agents", "skills", ".skillman.json"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove obsolete project state: %w", err)
	}
	return nil
}

func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temp := path + ".tmp"
	if err := os.WriteFile(temp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(temp, path)
}

func ensureRepo(ctx context.Context, cache, repoURL string) error {
	if _, err := os.Stat(filepath.Join(cache, ".git")); errors.Is(err, os.ErrNotExist) {
		return cloneRepo(ctx, cache, repoURL)
	} else if err != nil {
		return fmt.Errorf("inspect repository cache: %w", err)
	}
	if !repoHasHEAD(ctx, cache) {
		if err := os.RemoveAll(cache); err != nil {
			return fmt.Errorf("remove incomplete repository cache: %w", err)
		}
		return cloneRepo(ctx, cache, repoURL)
	}
	return updateRepo(ctx, cache)
}

func cloneRepo(ctx context.Context, cache, repoURL string) error {
	if err := os.MkdirAll(filepath.Dir(cache), 0o755); err != nil {
		return fmt.Errorf("create repository cache: %w", err)
	}
	staging := cache + ".partial"
	if err := os.RemoveAll(staging); err != nil {
		return fmt.Errorf("clear incomplete repository cache: %w", err)
	}
	if err := runGit(ctx, "", "clone", "--depth=1", "--filter=blob:none", "--no-checkout", repoURL, staging); err != nil {
		_ = os.RemoveAll(staging)
		return err
	}
	if err := os.RemoveAll(cache); err != nil {
		_ = os.RemoveAll(staging)
		return fmt.Errorf("replace repository cache: %w", err)
	}
	if err := os.Rename(staging, cache); err != nil {
		_ = os.RemoveAll(staging)
		return fmt.Errorf("promote repository cache: %w", err)
	}
	return nil
}

func repoHasHEAD(ctx context.Context, dir string) bool {
	_, err := gitOutput(ctx, dir, "rev-parse", "--verify", "HEAD")
	return err == nil
}

func updateRepo(ctx context.Context, cache string) error {
	if _, err := os.Stat(filepath.Join(cache, ".git")); err != nil {
		return fmt.Errorf("repository cache is missing; pull the source again: %w", err)
	}
	if !repoHasHEAD(ctx, cache) {
		return fmt.Errorf("repository cache is incomplete; pull the source again")
	}
	return runGit(ctx, cache, "pull", "--ff-only")
}

func runGit(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}

func slug(value string) string {
	var out strings.Builder
	dash := false
	for _, char := range strings.ToLower(value) {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' {
			out.WriteRune(char)
			dash = false
		} else if out.Len() > 0 && !dash {
			out.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(out.String(), "-")
}

func shortHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:4])
}
