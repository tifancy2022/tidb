package main

import (
	_ "embed"
	"sort"
	"strings"
)

//go:embed app.html
var appHtml []byte

//go:embed teams.txt
var teams []byte

var teamNames []string

func init() {
	teamNames = strings.Split(strings.TrimSpace(string(teams)), "\n")
	for i := range teamNames {
		teamNames[i] = strings.TrimSpace(teamNames[i])
	}
	sort.Strings(teamNames)
}
