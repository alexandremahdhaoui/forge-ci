package clidriver

import (
	"fmt"
	"sort"
	"strings"

	"github.com/alexandremahdhaoui/forge-ci/internal/controller/reconcilecontroller"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
	"github.com/alexandremahdhaoui/forge-ci/pkg/config"
)

const (
	classPassed  = "passed"
	classFailed  = "failed"
	classPending = "pending"
	classRunning = "running"
)

func mermaid(p config.Pipeline, report reconcilecontroller.Report) string {
	var b strings.Builder

	b.WriteString("flowchart LR\n")

	writeTriggers(&b, p)
	writeRevision(&b, report)
	writeStages(&b, p, report)
	writeEngines(&b, p)
	writeClasses(&b)

	return b.String()
}

func writeTriggers(b *strings.Builder, p config.Pipeline) {
	if len(p.Triggers) == 0 {
		fmt.Fprintf(b, "  trigger[\"manual\"]\n")

		return
	}

	fmt.Fprintf(b, "  trigger[\"%s\"]\n", strings.Join(p.Triggers, "<br/>"))
}

func writeRevision(b *strings.Builder, report reconcilecontroller.Report) {
	names := make([]string, 0, len(report.Revision.Repos))
	for name := range report.Revision.Repos {
		names = append(names, name)
	}

	sort.Strings(names)

	lines := make([]string, 0, len(names)+1)
	lines = append(lines, "revision "+shortOr(report.Revision.ID, "none"))

	for _, name := range names {
		lines = append(lines, name+" "+short(report.Revision.Repos[name]))
	}

	fmt.Fprintf(b, "  revision[\"%s\"]\n", strings.Join(lines, "<br/>"))
	fmt.Fprintf(b, "  trigger --> revision\n")
}

func writeStages(b *strings.Builder, p config.Pipeline, report reconcilecontroller.Report) {
	byStage := map[string]reconcilecontroller.StageReport{}
	for _, s := range report.Stages {
		byStage[s.Name] = s
	}

	previous := "revision"

	for i, stage := range p.Stages {
		id := fmt.Sprintf("s%d", i)
		observed := byStage[stage.Name]

		fmt.Fprintf(b, "  subgraph %s[\"%s\"]\n", id, stage.Name)

		for j, sub := range stage.Substages {
			subID := fmt.Sprintf("%s_%d", id, j)
			status := statusOf(observed, sub.Name)

			fmt.Fprintf(b, "    %s[\"%s<br/><i>%s</i><br/>%s\"]:::%s\n",
				subID, sub.Name, strings.Join(sub.Targets, ", "), status, status)

			for k, gate := range sub.Gates {
				gateID := fmt.Sprintf("%s_g%d", subID, k)
				gateStatus := gateStatusOf(observed, sub.Name, gate)

				fmt.Fprintf(b, "    %s{{\"gate %s<br/>%s\"}}:::%s\n", gateID, gate, gateStatus, gateStatus)
				fmt.Fprintf(b, "    %s --> %s\n", subID, gateID)
			}
		}

		fmt.Fprintf(b, "  end\n")

		promotion := stage.Promotion
		if promotion == "" {
			promotion = "all substages"
		}

		fmt.Fprintf(b, "  %s -->|%s| %s\n", previous, promotion, id)

		previous = id
	}

	fmt.Fprintf(b, "  %s --> done([\"released\"])\n", previous)
}

func writeEngines(b *strings.Builder, p config.Pipeline) {
	if len(p.Engines) == 0 {
		return
	}

	fmt.Fprintf(b, "  subgraph engines[\"engines and managers\"]\n")
	fmt.Fprintf(b, "    direction TB\n")

	for i, e := range p.Engines {
		fmt.Fprintf(b, "    e%d[\"%s<br/><i>%s</i><br/>via %s\"]\n", i, e.Alias, e.Type, e.Manager)
	}

	fmt.Fprintf(b, "  end\n")
}

func writeClasses(b *strings.Builder) {
	fmt.Fprintf(b, "  classDef %s fill:#1b5e20,stroke:#66bb6a,color:#fff\n", classPassed)
	fmt.Fprintf(b, "  classDef %s fill:#7f1d1d,stroke:#ef5350,color:#fff\n", classFailed)
	fmt.Fprintf(b, "  classDef %s fill:#4a3800,stroke:#ffca28,color:#fff\n", classPending)
	fmt.Fprintf(b, "  classDef %s fill:#0d3c61,stroke:#42a5f5,color:#fff\n", classRunning)
}

func statusOf(stage reconcilecontroller.StageReport, substage string) string {
	for _, run := range stage.Runs {
		if run.Substage == substage {
			return string(run.Status)
		}
	}

	return string(citypes.StatusPending)
}

func gateStatusOf(stage reconcilecontroller.StageReport, substage, gate string) string {
	for _, run := range stage.Runs {
		if run.Substage != substage {
			continue
		}

		for _, g := range run.Gates {
			if g.Alias == gate {
				return string(g.Status)
			}
		}
	}

	return string(citypes.StatusPending)
}

func shortOr(s, fallback string) string {
	if s == "" {
		return fallback
	}

	return short(s)
}
