package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "skillman:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	lib, err := OpenLibrary()
	if err != nil {
		return err
	}
	project, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("find current directory: %w", err)
	}
	if len(args) == 0 {
		return runTUI(ctx, lib, project)
	}

	switch args[0] {
	case "pull":
		source, names, err := parsePullArgs(args[1:])
		if err != nil {
			return err
		}
		skills, err := lib.Pull(ctx, source, names)
		if err != nil {
			return err
		}
		for _, skill := range skills {
			fmt.Printf("pulled %s  %s\n", skill.Name, skill.Source)
		}
	case "find":
		query := strings.TrimSpace(strings.Join(args[1:], " "))
		if query == "" {
			return usageError("find <query>")
		}
		results, err := lib.Find(ctx, query)
		if err != nil {
			return err
		}
		if len(results) == 0 {
			fmt.Printf("no skills found for %q\n", query)
			return nil
		}
		return runSearchTUI(ctx, lib, project, query, results)
	case "link":
		if len(args) != 2 {
			return usageError("link <skill-directory>")
		}
		skill, err := lib.Link(args[1])
		if err != nil {
			return err
		}
		fmt.Printf("linked %s  %s\n", skill.Name, skill.Source)
	case "update":
		if len(args) > 2 {
			return usageError("update [name-or-source]")
		}
		selector := ""
		if len(args) == 2 {
			selector = args[1]
		}
		skills, err := lib.Update(ctx, selector)
		if err != nil {
			return err
		}
		fmt.Printf("updated %d skill(s)\n", len(skills))
	case "add":
		if len(args) != 2 {
			return usageError("add <name-or-source>")
		}
		if err := lib.Activate(ctx, project, args[1]); err != nil {
			return err
		}
		fmt.Printf("added %s to %s\n", args[1], filepath.Join(project, ".agents", "skills"))
	case "remove":
		if len(args) != 2 {
			return usageError("remove <name-or-source>")
		}
		if err := lib.Deactivate(ctx, project, args[1]); err != nil {
			return err
		}
		fmt.Printf("removed %s\n", args[1])
	case "sync":
		if len(args) != 1 {
			return usageError("sync")
		}
		if err := lib.Sync(ctx, project); err != nil {
			return err
		}
		fmt.Println("synced active skills")
	case "list":
		if len(args) != 1 {
			return usageError("list")
		}
		active, err := lib.Active(ctx, project)
		if err != nil {
			return err
		}
		skills, err := lib.List(ctx)
		if err != nil {
			return err
		}
		for _, skill := range skills {
			marker := " "
			if active[skill.ID] {
				marker = "●"
			}
			fmt.Printf("%s %-24s %s\n", marker, skill.Name, skill.Source)
		}
	case "help", "-h", "--help":
		fmt.Print(helpText)
	default:
		return fmt.Errorf("unknown command %q\n\n%s", args[0], strings.TrimSpace(helpText))
	}
	return nil
}

func parsePullArgs(args []string) (source string, names []string, err error) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--skill" || arg == "-s":
			if i+1 >= len(args) {
				return "", nil, usageError("pull <source> [--skill name]...")
			}
			i++
			names = append(names, args[i])
		case strings.HasPrefix(arg, "--skill="):
			names = append(names, strings.TrimPrefix(arg, "--skill="))
		case strings.HasPrefix(arg, "-s="):
			names = append(names, strings.TrimPrefix(arg, "-s="))
		case arg == "-h" || arg == "--help":
			return "", nil, usageError("pull <source> [--skill name]...")
		case strings.HasPrefix(arg, "-"):
			return "", nil, fmt.Errorf("unknown flag %s\n\nusage: skillman pull <source> [--skill name]...", arg)
		case source != "":
			return "", nil, usageError("pull <source> [--skill name]...")
		default:
			source = arg
		}
	}
	if source == "" {
		return "", nil, usageError("pull <source> [--skill name]...")
	}
	if !strings.HasPrefix(source, "git@") {
		if repo, name, ok := strings.Cut(source, "@"); ok && name != "" {
			source = repo
			names = append(names, name)
		}
	}
	return source, names, nil
}

func usageError(command string) error {
	return fmt.Errorf("usage: skillman %s", command)
}

const helpText = `skillman manages a central agent skill library.

Usage:
  skillman                         open the TUI for this project
  skillman find <query>            browse skills.sh results and install or update one
  skillman pull <source> [--skill name]...
  skillman link <skill-directory>  symlink a local skill into the library
  skillman update [name-or-source]
  skillman add <name-or-source>    copy a skill into .agents/skills
  skillman remove <name-or-source>
  skillman sync                    refresh active copies
  skillman list
`
