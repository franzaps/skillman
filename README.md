# skillman

Keep reusable agent skills in one place. Then add the skills you want to each
project.

`skillman` copies active skills into `.agents/skills/` in your current
directory. It does not run a background service or keep a database.

## What you need

- Go 1.24 or newer
- Git
- A terminal

## Install

Install the latest version directly from GitHub:

```sh
go install github.com/franzaps/skillman@latest
```

Or clone this repository and install from its directory:

```sh
go install .
```

Make sure Go's bin directory is on your `PATH`. Then check the install:

```sh
skillman --help
```

## Start here

Go to the project where you want to use skills:

```sh
cd ~/code/my-project
```

Find a skill, choose one from the list, and press `Enter`:

```sh
skillman find testing
```

The selected skill is downloaded and added to this project. Open the library
at any time with:

```sh
skillman
```

Press `Space` to add or remove the selected skill for the current project.

## Common commands

### Download skills from a repository

Download every skill found in a GitHub repository:

```sh
skillman pull vercel-labs/agent-skills
```

Download one named skill:

```sh
skillman pull juliusbrussee/caveman --skill caveman
```

You can also use a full Git clone URL. Use this for repositories outside
GitHub:

```sh
skillman pull https://git.example.com/team/skills.git --skill review
```

After downloading, add a skill to the current project:

```sh
skillman add caveman
```

### Use a skill you are writing locally

Link a local skill directory into your library:

```sh
skillman link ~/code/my-skill
```

The directory must contain `SKILL.md`. Add the linked skill to the current
project with `skillman add <name>`.

### See and manage project skills

```sh
skillman list          # show library skills; ● means active here
skillman add <name>    # add one to this project
skillman remove <name> # remove one from this project
skillman sync          # copy the current library versions to this project
```

### Update downloaded skills

```sh
skillman update           # update every downloaded skill
skillman update caveman   # update one skill
```

Run `skillman sync` afterwards to update the copies already active in the
current project.

## Terminal interface

Run `skillman` with no command to open the skill list.

| Key | Action |
| --- | --- |
| `↑` / `↓` or `j` / `k` | Move |
| `Space` | Add or remove the selected skill in this project |
| `u` | Update the selected library skill |
| `s` | Sync all active skills to this project |
| `q`, `Esc`, or `Ctrl+C` | Quit |

## Where files go

- Your library: `~/.skillman`
- Downloaded repository cache: `~/.skillman/.repos/`
- Linked local skills: `~/.skillman/skills/`
- Skills active in a project: `<project>/.agents/skills/`

Set `SKILLMAN_HOME` to use a different library location:

```sh
export SKILLMAN_HOME="$HOME/.local/share/skillman"
```

## Important details

- `pull owner/repo` means a GitHub repository.
- With no `--skill` flag, `pull` keeps every skill it finds. Use
  `--skill '*'` for the same result.
- You can repeat `--skill` to choose several skills.
- Adding a skill copies it into the project. It is not a symlink.
- Linked local skills stay linked in the library, but their project copies do
  not change until you run `skillman sync`.
- Two skills may have the same name. When that happens, use the full source
  shown by `skillman list`, such as `owner/repo/path/to/skill`.

## Trust downloaded skills

Skills are instructions and may include scripts or commands. Read skills from
new sources before using them. `skillman` downloads and copies files; it does
not review them for safety.
