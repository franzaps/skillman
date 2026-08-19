package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLinkedSkillActivatesAsCopy(t *testing.T) {
	t.Setenv("SKILLMAN_HOME", filepath.Join(t.TempDir(), "home"))
	source := filepath.Join(t.TempDir(), "source")
	writeTestSkill(t, source, "demo")
	if err := os.WriteFile(filepath.Join(source, "notes.txt"), []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}

	lib, err := OpenLibrary()
	if err != nil {
		t.Fatal(err)
	}
	skill, err := lib.Link(source)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(lib.skillPath(skill)); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("library skill is not a symlink: info=%v err=%v", info, err)
	}

	project := t.TempDir()
	if err := lib.Activate(t.Context(), project, skill.ID); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(project, ".agents", "skills", "demo")
	if info, err := os.Lstat(dest); err != nil || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("active skill should be a real directory: info=%v err=%v", info, err)
	}
	if err := os.WriteFile(filepath.Join(source, "notes.txt"), []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dest, "notes.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "original" {
		t.Fatalf("active copy changed through source: %q", data)
	}
}

func TestActivationUsesFilesystemState(t *testing.T) {
	t.Setenv("SKILLMAN_HOME", filepath.Join(t.TempDir(), "home"))
	source := filepath.Join(t.TempDir(), "source")
	writeTestSkill(t, source, "demo")
	if err := os.WriteFile(filepath.Join(source, "notes.txt"), []byte("library"), 0o644); err != nil {
		t.Fatal(err)
	}

	lib, err := OpenLibrary()
	if err != nil {
		t.Fatal(err)
	}
	skill, err := lib.Link(source)
	if err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	dest := filepath.Join(project, ".agents", "skills", "demo")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "notes.txt"), []byte("existing"), 0o644); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(project, ".agents", "skills", ".skillman.json")
	if err := os.WriteFile(state, []byte(`{"skills":{"old":"demo"}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := lib.Activate(t.Context(), project, skill.ID); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dest, "notes.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "library" {
		t.Fatalf("destination = %q, want library", data)
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatalf("obsolete state still exists: %v", err)
	}
	active, err := lib.Active(t.Context(), project)
	if err != nil {
		t.Fatal(err)
	}
	if !active[skill.ID] {
		t.Fatal("skill directory was not recognized as active")
	}
	if err := lib.Deactivate(t.Context(), project, skill.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("active skill still exists: %v", err)
	}
}

func TestDeactivateRemovesSkillOutsideLibrary(t *testing.T) {
	t.Setenv("SKILLMAN_HOME", filepath.Join(t.TempDir(), "home"))
	lib, err := OpenLibrary()
	if err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	dest := filepath.Join(project, ".agents", "skills", "other-skill")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "SKILL.md"), []byte("# Other skill\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := lib.Deactivate(t.Context(), project, "other-skill"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("skill directory still exists: %v", err)
	}
}

func TestSyncUsesExistingSkillDirectories(t *testing.T) {
	t.Setenv("SKILLMAN_HOME", filepath.Join(t.TempDir(), "home"))
	source := filepath.Join(t.TempDir(), "source")
	writeTestSkill(t, source, "demo")
	if err := os.WriteFile(filepath.Join(source, "notes.txt"), []byte("before"), 0o644); err != nil {
		t.Fatal(err)
	}

	lib, err := OpenLibrary()
	if err != nil {
		t.Fatal(err)
	}
	skill, err := lib.Link(source)
	if err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	if err := lib.Activate(t.Context(), project, skill.ID); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "notes.txt"), []byte("after"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := lib.Sync(t.Context(), project); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(project, ".agents", "skills", "demo", "notes.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "after" {
		t.Fatalf("synced copy = %q, want after", data)
	}
}

func TestResolveDisambiguatesDuplicateNames(t *testing.T) {
	skills := []Skill{
		{ID: "one", Name: "review", Source: "owner/one/skills/review"},
		{ID: "two", Name: "review", Source: "owner/two/skills/review"},
	}

	_, err := resolveSkill(skills, "review")
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("expected ambiguity error, got %v", err)
	}
	skill, err := resolveSkill(skills, "owner/two/skills/review")
	if err != nil {
		t.Fatal(err)
	}
	if skill.ID != "two" {
		t.Fatalf("resolved %q, want two", skill.ID)
	}
}

func TestDiscoverSkillsAtAnyDepth(t *testing.T) {
	root := t.TempDir()
	deep := filepath.Join(root, "plugins", "writing", "skills", "editor")
	writeTestSkill(t, deep, "editor")

	paths, err := discoverSkills(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || paths[0] != deep {
		t.Fatalf("discovered %v, want %s", paths, deep)
	}
}

func TestListSkipsIncompleteRepoCache(t *testing.T) {
	t.Setenv("SKILLMAN_HOME", filepath.Join(t.TempDir(), "home"))
	lib, err := OpenLibrary()
	if err != nil {
		t.Fatal(err)
	}

	good := filepath.Join(lib.home, repoCacheDir, "good")
	writeTestSkill(t, filepath.Join(good, "skills", "remote"), "remote-skill")
	initGitRepo(t, good, "https://github.com/acme/tools.git")

	initIncompleteGitRepo(t, filepath.Join(lib.home, repoCacheDir, "bad"), "https://github.com/acme/broken.git")
	initIncompleteGitRepo(t, filepath.Join(lib.home, repoCacheDir, "good.partial"), "https://github.com/acme/partial.git")

	skills, err := lib.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(skills) != 1 || skills[0].Name != "remote-skill" {
		t.Fatalf("listed %+v, want only remote-skill", skills)
	}
}

func TestEnsureRepoReplacesIncompleteCache(t *testing.T) {
	remote := t.TempDir()
	writeTestSkill(t, filepath.Join(remote, "skills", "demo"), "demo")
	initGitRepo(t, remote, "https://github.com/acme/demo.git")

	cache := filepath.Join(t.TempDir(), "cache")
	initIncompleteGitRepo(t, cache, "https://github.com/acme/demo.git")

	if err := ensureRepo(t.Context(), cache, "file://"+remote); err != nil {
		t.Fatal(err)
	}
	if !repoHasHEAD(t.Context(), cache) {
		t.Fatal("replaced cache still has no HEAD")
	}
}

func TestCloneRepoRemovesStagingAfterCancel(t *testing.T) {
	remote := t.TempDir()
	writeTestSkill(t, filepath.Join(remote, "skills", "demo"), "demo")
	initGitRepo(t, remote, "https://github.com/acme/demo.git")

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	cache := filepath.Join(t.TempDir(), "cache")
	if err := cloneRepo(ctx, cache, "file://"+remote); err == nil {
		t.Fatal("expected canceled clone to fail")
	}
	if _, err := os.Stat(cache); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cache should not exist after canceled clone: %v", err)
	}
	if _, err := os.Stat(cache + ".partial"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging cache should be removed after canceled clone: %v", err)
	}
}

func TestListReadsReposAndSymlinks(t *testing.T) {
	t.Setenv("SKILLMAN_HOME", filepath.Join(t.TempDir(), "home"))
	lib, err := OpenLibrary()
	if err != nil {
		t.Fatal(err)
	}

	local := filepath.Join(t.TempDir(), "local")
	writeTestSkill(t, local, "local-skill")
	if _, err := lib.Link(local); err != nil {
		t.Fatal(err)
	}

	cache := filepath.Join(lib.home, repoCacheDir, "testhash")
	writeTestSkill(t, filepath.Join(cache, "skills", "remote"), "remote-skill")
	initGitRepo(t, cache, "https://github.com/acme/tools.git")

	skills, err := lib.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(skills) != 2 {
		t.Fatalf("listed %d skills, want 2: %+v", len(skills), skills)
	}
}

func TestListReadsSkillsInLibraryHome(t *testing.T) {
	t.Setenv("SKILLMAN_HOME", filepath.Join(t.TempDir(), "home"))
	lib, err := OpenLibrary()
	if err != nil {
		t.Fatal(err)
	}

	writeTestSkill(t, filepath.Join(lib.home, "pip"), "pip")
	if err := os.Mkdir(filepath.Join(lib.home, "adx"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/no/such/skill", filepath.Join(lib.home, "broken")); err != nil {
		t.Fatal(err)
	}

	source := filepath.Join(t.TempDir(), "slop-defender")
	writeTestSkill(t, source, "slop-defender")
	if err := os.Symlink(source, filepath.Join(lib.home, "slop-defender")); err != nil {
		t.Fatal(err)
	}

	skills, err := lib.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(skills) != 2 {
		t.Fatalf("listed %d skills, want 2: %+v", len(skills), skills)
	}

	project := t.TempDir()
	if err := lib.Activate(t.Context(), project, "pip"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(project, ".agents", "skills", "pip", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := lib.Update(t.Context(), "pip"); err != nil {
		t.Fatal(err)
	}
}

func TestParsePullArgs(t *testing.T) {
	source, names, err := parsePullArgs([]string{"https://github.com/juliusbrussee/caveman", "--skill", "caveman"})
	if err != nil {
		t.Fatal(err)
	}
	if source != "https://github.com/juliusbrussee/caveman" || strings.Join(names, ",") != "caveman" {
		t.Fatalf("got %q %v", source, names)
	}

	source, names, err = parsePullArgs([]string{"--skill", "frontend-design", "-s", "skill-creator", "vercel-labs/agent-skills"})
	if err != nil {
		t.Fatal(err)
	}
	if source != "vercel-labs/agent-skills" || strings.Join(names, ",") != "frontend-design,skill-creator" {
		t.Fatalf("got %q %v", source, names)
	}

	source, names, err = parsePullArgs([]string{"affaan-m/ecc@golang-patterns"})
	if err != nil {
		t.Fatal(err)
	}
	if source != "affaan-m/ecc" || strings.Join(names, ",") != "golang-patterns" {
		t.Fatalf("got %q %v", source, names)
	}
}

func TestPreferCanonicalSkills(t *testing.T) {
	skills := preferCanonicalSkills([]Skill{
		{Name: "caveman", RepoURL: "https://github.com/juliusbrussee/caveman.git", RepoPath: "plugins/caveman/skills/caveman"},
		{Name: "caveman", RepoURL: "https://github.com/juliusbrussee/caveman.git", RepoPath: "skills/caveman"},
		{Name: "ponytail", RepoURL: "https://github.com/dietrichgebert/ponytail.git", RepoPath: ".openclaw/skills/ponytail"},
		{Name: "ponytail", RepoURL: "https://github.com/dietrichgebert/ponytail.git", RepoPath: "skills/ponytail"},
	})
	if len(skills) != 2 {
		t.Fatalf("got %d skills, want 2: %+v", len(skills), skills)
	}
	if skills[0].RepoPath != "skills/caveman" || skills[1].RepoPath != "skills/ponytail" {
		t.Fatalf("got %+v", skills)
	}
}

func TestSelectPulledSkills(t *testing.T) {
	skills := []Skill{
		{Name: "caveman", RepoPath: "caveman"},
		{Name: "other", RepoPath: "skills/other"},
	}
	selected, err := selectPulledSkills(skills, []string{"caveman"})
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 1 || selected[0].Name != "caveman" {
		t.Fatalf("selected %+v", selected)
	}
	if _, err := selectPulledSkills(skills, []string{"missing"}); err == nil {
		t.Fatal("expected missing skill error")
	}
}

func TestParseSource(t *testing.T) {
	tests := []struct {
		in, repoURL, subpath, label string
	}{
		{"affaan-m/ecc/skills/golang-patterns", "https://github.com/affaan-m/ecc.git", "skills/golang-patterns", "affaan-m/ecc"},
		{"https://github.com/affaan-m/ecc/skills/golang-patterns", "https://github.com/affaan-m/ecc.git", "skills/golang-patterns", "affaan-m/ecc"},
		{"https://github.com/affaan-m/ecc.git", "https://github.com/affaan-m/ecc.git", "", "affaan-m/ecc"},
		{"https://github.com/affaan-m/ecc/tree/main/skills/golang-patterns", "https://github.com/affaan-m/ecc.git", "skills/golang-patterns", "affaan-m/ecc"},
		{"git@github.com:affaan-m/ecc.git", "https://github.com/affaan-m/ecc.git", "", "affaan-m/ecc"},
		{"https://gitlab.com/acme/tools", "https://gitlab.com/acme/tools.git", "", "gitlab.com/acme/tools"},
		{"https://gitlab.com/acme/tools/skills/foo", "https://gitlab.com/acme/tools.git", "skills/foo", "gitlab.com/acme/tools"},
		{"https://gitlab.com/group/sub/project.git/skills/foo", "https://gitlab.com/group/sub/project.git", "skills/foo", "gitlab.com/group/sub/project"},
		{"git@gitlab.com:acme/tools.git", "git@gitlab.com:acme/tools.git", "", "gitlab.com/acme/tools"},
		{"https://codeberg.org/user/repo/src/branch/main/skills/x", "https://codeberg.org/user/repo.git", "skills/x", "codeberg.org/user/repo"},
	}
	for _, test := range tests {
		repoURL, subpath, label, err := parseSource(test.in)
		if err != nil {
			t.Fatalf("%s: %v", test.in, err)
		}
		if repoURL != test.repoURL || subpath != test.subpath || label != test.label {
			t.Fatalf("%s: got %q %q %q, want %q %q %q", test.in, repoURL, subpath, label, test.repoURL, test.subpath, test.label)
		}
	}
}

func TestListRepoHonorsPulledSubpath(t *testing.T) {
	t.Setenv("SKILLMAN_HOME", filepath.Join(t.TempDir(), "home"))
	lib, err := OpenLibrary()
	if err != nil {
		t.Fatal(err)
	}

	cache := filepath.Join(lib.home, repoCacheDir, "testhash")
	writeTestSkill(t, filepath.Join(cache, "skills", "golang-patterns"), "golang-patterns")
	writeTestSkill(t, filepath.Join(cache, "skills", "other"), "other")
	initGitRepo(t, cache, "https://github.com/affaan-m/ecc.git")
	if err := rememberRepoPath(cache, "skills/golang-patterns"); err != nil {
		t.Fatal(err)
	}

	skills, err := lib.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(skills) != 1 || skills[0].Name != "golang-patterns" {
		t.Fatalf("listed %+v, want only golang-patterns", skills)
	}

	if err := rememberRepoPath(cache, ""); err != nil {
		t.Fatal(err)
	}
	skills, err = lib.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(skills) != 2 {
		t.Fatalf("listed %d skills after unrestricted pull, want 2: %+v", len(skills), skills)
	}
}

func TestCheckoutSkillManifestsAfterNoCheckoutClone(t *testing.T) {
	remote := t.TempDir()
	writeTestSkill(t, filepath.Join(remote, "skills", "refero-design"), "refero-design")
	if err := os.WriteFile(filepath.Join(remote, "README.md"), []byte("repo"), 0o644); err != nil {
		t.Fatal(err)
	}
	initGitRepo(t, remote, "https://github.com/acme/refero.git")

	cache := t.TempDir()
	remoteURL := "file://" + remote
	if err := runGit(t.Context(), "", "clone", "--depth=1", "--filter=blob:none", "--no-checkout", remoteURL, cache); err != nil {
		t.Fatal(err)
	}
	if err := checkoutSkillManifests(t.Context(), cache, ""); err != nil {
		t.Fatal(err)
	}
	paths, err := discoverSkills(cache)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(cache, "skills", "refero-design")
	if len(paths) != 1 || paths[0] != want {
		t.Fatalf("discovered %v, want %s", paths, want)
	}
}

func TestSparseCheckoutKeepsOnlySkillDirectories(t *testing.T) {
	cache := t.TempDir()
	writeTestSkill(t, filepath.Join(cache, "skills", "editor"), "editor")
	writeTestSkill(t, filepath.Join(cache, "skills", "writer"), "writer")
	if err := os.WriteFile(filepath.Join(cache, "README.md"), []byte("repository file"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(cache, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, "docs", "guide.md"), []byte("not a skill"), 0o644); err != nil {
		t.Fatal(err)
	}
	initGitRepo(t, cache, "https://github.com/acme/skills.git")

	if err := checkoutSkillDirectories(t.Context(), cache, []string{"skills/editor"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cache, "skills", "editor", "SKILL.md")); err != nil {
		t.Fatalf("selected skill was not checked out: %v", err)
	}
	for _, path := range []string{
		filepath.Join(cache, "README.md"),
		filepath.Join(cache, "docs", "guide.md"),
		filepath.Join(cache, "skills", "writer", "SKILL.md"),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s should not be checked out: %v", path, err)
		}
	}
}

func TestSearchSkillsReturnsTopFive(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if query := request.URL.Query().Get("q"); query != "go testing" {
			t.Errorf("query = %q, want %q", query, "go testing")
		}
		if limit := request.URL.Query().Get("limit"); limit != "5" {
			t.Errorf("limit = %q, want 5", limit)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"skills":[
			{"name":"one","skillId":"one","source":"acme/one","installs":5},
			{"name":"two","skillId":"two","source":"acme/two","installs":4},
			{"name":"three","skillId":"three","source":"acme/three","installs":3},
			{"name":"four","skillId":"four","source":"acme/four","installs":2},
			{"name":"five","skillId":"five","source":"acme/five","installs":1},
			{"name":"six","skillId":"six","source":"acme/six","installs":0}
		]}`))
	}))
	defer server.Close()

	results, err := searchSkills(t.Context(), server.URL, "go testing", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 5 {
		t.Fatalf("got %d results, want 5", len(results))
	}
	if results[0].SkillID != "one" || results[4].Name != "five" {
		t.Fatalf("unexpected results: %+v", results)
	}
}

func TestSearchSkillsRejectsEmptyQuery(t *testing.T) {
	_, err := searchSkills(t.Context(), "https://skills.sh/api/search", " ", 5)
	if err == nil || !strings.Contains(err.Error(), "query") {
		t.Fatalf("expected empty query error, got %v", err)
	}
}

func TestMarkInstalledSearchResults(t *testing.T) {
	results := markInstalledSearchResults([]SearchResult{
		{Name: "demo", Source: "acme/tools/skills/demo"},
		{Name: "other", Source: "acme/tools/skills/other"},
	}, []Skill{
		{
			ID:      "acme/tools:skills/demo",
			Name:    "demo",
			RepoURL: "https://github.com/acme/tools.git",
		},
	})

	if results[0].installedID != "acme/tools:skills/demo" {
		t.Fatalf("installed ID = %q, want demo skill", results[0].installedID)
	}
	if results[1].installedID != "" {
		t.Fatalf("other result marked as installed: %q", results[1].installedID)
	}
}

func writeTestSkill(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + name + "\n---\n# Test\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func initIncompleteGitRepo(t *testing.T, dir, remote string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	cmd = exec.Command("git", "remote", "add", "origin", remote)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v: %s", err, out)
	}
}

func initGitRepo(t *testing.T, dir, remote string) {
	t.Helper()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	run("init")
	run("remote", "add", "origin", remote)
	run("add", ".")
	run("-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-m", "init")
}
