//go:build bench

package main

import (
	"testing"
	"time"
)

func TestSummarizeUsesNearestRank(t *testing.T) {
	var durations []time.Duration
	for value := 100; value >= 1; value-- {
		durations = append(durations, time.Duration(value)*time.Millisecond)
	}
	stats := summarize(durations)
	if stats.Count != 100 || stats.P50 != 50 || stats.P95 != 95 || stats.P99 != 99 || stats.Max != 100 {
		t.Fatalf("unexpected stats %+v", stats)
	}
}

func TestParseCPUTimeReadsPSFormats(t *testing.T) {
	cases := map[string]float64{"0:01.25": 1.25, "12:03.50": 723.5, "1:02:03.00": 3723}
	for value, want := range cases {
		got, err := parseCPUTime(value)
		if err != nil || got != want {
			t.Errorf("parseCPUTime(%q) = %v, %v; want %v", value, got, err, want)
		}
	}
}

func TestParseProcStatHandlesSpacesInCommandName(t *testing.T) {
	line := "42 (python3 -I x) S 1 4242 4242 0 -1 4194560 100 0 0 0 250 50 0 0 20 0 3 0 1000 123456 2048 rest"
	info, ok := parseProcStat(line, 4)
	if !ok {
		t.Fatal("line not parsed")
	}
	if info.group != 4242 || info.cpuSeconds != 3 || info.rssKB != 8192 {
		t.Fatalf("unexpected info %+v", info)
	}
}

func TestFillPlannerKeepsTaskRules(t *testing.T) {
	members := []participantRow{{id: 1, role: roleLead}, {id: 2}, {id: 3}}
	planner, err := newFillPlanner(members)
	if err != nil {
		t.Fatal(err)
	}
	owners := map[int64]int64{}
	for id := int64(1); id <= 5000; id++ {
		row := planner.next(id, id)
		switch row.kind {
		case "task":
			if row.author != 1 || row.recipient == 0 || row.dueMinutes == 0 {
				t.Fatalf("invalid task row %+v", row)
			}
			owners[id] = row.recipient
		case "done":
			if owners[row.replyTo] != row.author {
				t.Fatalf("done row %+v not by the task owner", row)
			}
		case "question":
			if row.recipient == 0 || row.recipient == row.author {
				t.Fatalf("invalid question row %+v", row)
			}
		}
	}
}
