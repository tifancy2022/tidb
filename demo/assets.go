package main

import (
	_ "embed"
	"sort"
	"strings"
)

//go:embed app.html
var appHtml []byte

//go:embed app_teams.txt
var appTeams []byte

//go:embed prod_teams.txt
var prodTeams []byte

var teamNames []string
var teamMapping = map[string]int{}

func init() {
	appTeamNames := strings.Split(strings.TrimSpace(string(appTeams)), "\n")
	for i := range appTeamNames {
		name := strings.TrimSpace(appTeamNames[i])
		teamNames = append(teamNames, name)
		teamMapping[name] = 1
	}
	prodTeamNames := strings.Split(strings.TrimSpace(string(prodTeams)), "\n")
	for i := range prodTeamNames {
		name := strings.TrimSpace(prodTeamNames[i])
		teamNames = append(teamNames, name)
		teamMapping[name] = 2
	}
	sort.Strings(teamNames)
}
