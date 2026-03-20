# git-ownership

Walks a git repo's full history and produces a single self-contained HTML file
showing how code ownership (surviving lines per author) evolved over time.

## Examples (click to open)

<table>
<tr>
<td align="center" width="50%">
<b>Linux kernel</b><br>
<a href="https://michaelmure.github.io/git-ownership/results/linux.html">
<img src="https://michaelmure.github.io/git-ownership/results/linux.png" alt="Linux kernel">
</a>
</td>
<td align="center" width="50%">
<b>git-bug</b><br>
<a href="https://michaelmure.github.io/git-ownership/results/git-bug.html">
<img src="https://michaelmure.github.io/git-ownership/results/git-bug.png" alt="git-bug">
</a>
</td>
</tr>
<tr>
<td align="center">
<b>Home Assistant</b><br>
<a href="https://michaelmure.github.io/git-ownership/results/homeassistant-core.html">
<img src="https://michaelmure.github.io/git-ownership/results/homeassistant-core.png" alt="Home Assistant">
</a>
</td>
<td align="center">
<b>Kubernetes</b><br>
<a href="https://michaelmure.github.io/git-ownership/results/kubernetes.html">
<img src="https://michaelmure.github.io/git-ownership/results/kubernetes.png" alt="Kubernetes">
</a>
</td>
</tr>
<tr>
<td align="center">
<b>Traefik</b><br>
<a href="https://michaelmure.github.io/git-ownership/results/traefik.html">
<img src="https://michaelmure.github.io/git-ownership/results/traefik.png" alt="Traefik">
</a>
</td>
<td align="center">
<b>GIMP</b><br>
<a href="https://michaelmure.github.io/git-ownership/results/gimp.html">
<img src="https://michaelmure.github.io/git-ownership/results/gimp.png" alt="GIMP">
</a>
</td>
</tr>
</table>

## Build

```
go build -o git-ownership .
```

No external dependencies.

## Usage

```
git-ownership /path/to/repo
```

This writes `<reponame>.html` in the current directory. Open it in a browser.

To exclude vendor or generated directories from ownership tracking:

```
git-ownership --exclude-regex '^vendor/' /path/to/repo
```

### Flags

| Flag               | Default           | Description                                                                  |
|--------------------|-------------------|------------------------------------------------------------------------------|
| `--branch`         | `HEAD`            | Branch/ref to analyse                                                        |
| `--output`         | `<reponame>.html` | Output file path                                                             |
| `--max-points`     | `1000`            | Max chart data points — commits are strided to fit (0 = record every commit) |
| `--max-graph`      | `50`              | Max authors included as individual chart datasets (0 = all)                  |
| `--folder`         | `10`              | Number of sub-folders to break down (0 = whole repo only)                    |
| `--workers`        | num CPUs          | Parallel `git log` workers                                                   |
| `--exclude-regex`  | _(none)_          | Exclude file paths matching this regex (e.g. `^vendor/`)                     |

## Output

A single fully self-contained `.html` file with Chart.js embedded — no
internet access required.

- **Hover** for a tooltip showing all authors at that commit, sorted by line count
- **Click** a band or legend entry to open the author panel (stats + sparkline)
- **Toggle** between Line Count / % Share and Commits / Time x-axis
- **Bands slider** controls how many authors get individual colored bands; the rest are grouped into Others — click any Others member for their individual sparkline
- **Folder dropdown** (when `--folder` > 0) switches between whole-repo and per-folder breakdowns
- **Search** by name or email to jump to any author
