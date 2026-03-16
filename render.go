// render.go — HTML template rendering.

package main

import (
	_ "embed"
	"encoding/json"
	"html/template"
	"log"
	"os"
	"time"
)

//go:embed template.html
var htmlTpl string

//go:embed chartjs.min.js
var chartJS string

type tplVars struct {
	Repo         string
	Branch       string
	ChartJS      template.JS
	TotalCommits int
	Samples      int
	Authors      int
	Generated    string
	DataJSON     template.JS
}

func renderHTML(outFile string, vars tplVars) {
	tmpl := template.Must(template.New("page").Parse(htmlTpl))
	f, err := os.Create(outFile)
	if err != nil {
		log.Fatalf("create output file: %v", err)
	}
	defer f.Close()
	if err := tmpl.Execute(f, vars); err != nil {
		log.Fatalf("render template: %v", err)
	}
}

func buildTemplateVars(repo, branch, outFile string, total int, snaps []Snapshot, cd chartData) tplVars {
	jsonBytes, err := json.Marshal(cd)
	if err != nil {
		log.Fatalf("json marshal: %v", err)
	}
	authorCount := 0
	if len(snaps) > 0 {
		authorCount = len(snaps[len(snaps)-1].Totals)
	}
	return tplVars{
		Repo:         repo,
		Branch:       branch,
		ChartJS:      template.JS(chartJS),
		TotalCommits: total,
		Samples:      len(snaps),
		Authors:      authorCount,
		Generated:    time.Now().Format("2006-01-02 15:04"),
		DataJSON:     template.JS(jsonBytes),
	}
}
