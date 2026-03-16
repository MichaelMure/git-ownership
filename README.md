# git-ownership

Walks a git repo's full history and produces a single self-contained HTML file
showing how code ownership (surviving lines per author) evolved over time.

## Examples

<!-- kubernetes/kubernetes -->
<!-- linux/linux -->
<!-- golang/go -->

## Build

```
go build -o git-ownership .
```

No external dependencies.

## Usage

```
./git-ownership --repo /path/to/repo
```

This writes `<reponame>.html` in the current directory. Open it in a browser.

### Flags

| Flag           | Default           | Description                                               |
|----------------|-------------------|-----------------------------------------------------------|
| `--repo`       | `.`               | Path to the git repository                                |
| `--branch`     | default branch    | Branch to analyse                                         |
| `--output`     | `<reponame>.html` | Output file path                                          |
| `--max-points` | `1000`            | Max chart data points (downsampled with LTTB)             |
| `--min-pct`    | `1.0`             | Authors who never exceeded this % are grouped into Others |
| `--workers`    | num CPUs          | Parallel `git log` workers                                |

## Output

A single fully self-contained `.html` file — Chart.js is embedded at build
time, so it works with no internet access. The chart is interactive:

- Hover to see a tooltip with all authors at that point in time, sorted by
  line count, with the band under the cursor highlighted.
- Click a band (or a legend entry) to open the author side panel with stats
  and a sparkline.
- Toggle between **% Share** and **Line Count** views.
- Search for any author by name or email.
