//go:build conformance

package conformance

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// The differential walk (port plan, "Tests", layer B) sends the same seeded sequence of
// valid and near-valid requests to a reference daemon and to the daemon under test, each
// with its own AGENTCY_HOME, and compares every normalised response and, at the end, a
// normalised dump of every database.
//
//	AGENTCY_REFERENCE_CORE_CMD="uv run python -I -m agentcy.mcp.core" \
//	AGENTCY_CORE_CMD="$PWD/bin/agentcy core" \
//	go test -tags conformance ./conformance/... -run TestDifferentialWalk -seeds 20 -steps 2000
//
// -seed replays one seed. Generated values (tokens, codes, IDs, digests) are normalised
// by order of first appearance, as in the goldens; every timestamp becomes <time>,
// because the two daemons read the clock a few milliseconds apart.
var (
	walkSeeds = flag.Int("seeds", 2, "number of differential walk seeds to run, from 1")
	walkSeed  = flag.Int64("seed", 0, "run only this differential walk seed")
	walkSteps = flag.Int("steps", 200, "requests per differential walk seed")
)

// referenceCommandVariable names the daemon the walk compares against.
const referenceCommandVariable = "AGENTCY_REFERENCE_CORE_CMD"

// walkTokenSettings gives the walk's models budgets, so status shows usage and balance.
const walkTokenSettings = `{"models": {
	"model-a": {"initial_tokens": 4000},
	"model-b": {"initial_tokens": 9000, "warn_percent": 50, "imbalance_gap_percent": 10, "reset_window_hours": 5}},
	"default_initial_tokens": 3000}`

func TestDifferentialWalk(t *testing.T) {
	reference := strings.Fields(os.Getenv(referenceCommandVariable))
	tested := strings.Fields(os.Getenv(coreCommandVariable))
	if len(reference) == 0 || len(tested) == 0 {
		t.Skipf("set %s and %s to the two daemon commands", referenceCommandVariable, coreCommandVariable)
	}
	seeds := []int64{}
	if *walkSeed != 0 {
		seeds = append(seeds, *walkSeed)
	} else {
		for seed := int64(1); seed <= int64(*walkSeeds); seed++ {
			seeds = append(seeds, seed)
		}
	}
	for _, seed := range seeds {
		t.Run(fmt.Sprintf("seed-%d", seed), func(t *testing.T) {
			t.Parallel()
			runWalk(t, seed, *walkSteps, [2][]string{reference, tested})
		})
	}
}

// walkSide is one of the two daemons of a walk with its normaliser.
type walkSide struct {
	name       string
	daemon     *daemon
	normalizer *normalizer
}

// runWalk runs one seed against both daemons and fails at the first difference.
func runWalk(t *testing.T, seed int64, steps int, commands [2][]string) {
	start := time.Now().Unix()
	directory := shortTempDir(t, "agentcy-work-")
	resolved := resolvePath(t, directory)
	var sides [2]*walkSide
	for index, name := range []string{"reference", "tested"} {
		server := startDaemonCommand(t, commands[index])
		side := &walkSide{name: name, daemon: server, normalizer: newNormalizer(start)}
		side.normalizer.addLiteral(server.home, "<home>")
		side.normalizer.addLiteral(resolved, "<checkout>")
		side.normalizer.addLiteral(directory, "<checkout>")
		side.normalizer.addLiteral(server.operatorKey(t), "<operator-key>")
		writeWalkSetting(t, server.home, "tokens.json", walkTokenSettings)
		sides[index] = side
	}
	succeeded := map[string]int{}
	refused := map[string]int{}
	generator := newWalkGenerator(seed, sha256Hex(resolved), directory, [2]string{sides[0].daemon.operatorKey(t), sides[1].daemon.operatorKey(t)})
	for step := 1; step <= steps; step++ {
		action := generator.next()
		var replies [2]walkReply
		var shown [2]string
		for index, side := range sides {
			request := generator.build(action.request, index)
			replies[index], shown[index] = sendWalkRequest(t, side, request, start)
		}
		if shown[0] != shown[1] {
			reportWalkDifference(t, seed, step, steps, action, sides, shown)
		}
		if action.observe != nil {
			action.observe(replies)
		}
		if replies[0].errorText != "" {
			refused[action.label+": "+replies[0].errorText]++
		} else {
			succeeded[action.label]++
		}
	}
	compareWalkDumps(t, seed, steps, sides, start)
	t.Logf("seed %d: %d steps, results by action %v, refusals by action %v", seed, steps, succeeded, refused)
}

// writeWalkSetting writes a private settings file into a daemon's home.
func writeWalkSetting(t *testing.T, home, name, content string) {
	t.Helper()
	if err := os.WriteFile(home+"/"+name, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// sendWalkRequest sends one request and returns the decoded reply and its normalised
// text.
func sendWalkRequest(t *testing.T, side *walkSide, request fields, start int64) (walkReply, string) {
	t.Helper()
	line, err := encodeRequest(request)
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}
	answer, err := exchange(side.daemon.socket, line, false)
	if err != nil {
		t.Fatalf("%s daemon: %v\n%s", side.name, err, side.daemon.output.String())
	}
	if answer == "" {
		return walkReply{}, "<no response>"
	}
	decoded, err := decodeOrdered([]byte(answer))
	if err != nil {
		t.Fatalf("%s daemon reply is not JSON: %v", side.name, err)
	}
	normalised := side.normalizer.value(coarseTimes(decoded, "", start), "")
	return parseWalkReply(answer), formatJSON(normalised, true)
}

// coarseTimes replaces every integer that looks like a Unix time near start with
// "<time>", keeping idle_seconds for the normaliser.
func coarseTimes(value any, key string, start int64) any {
	switch typed := value.(type) {
	case orderedObject:
		result := make(orderedObject, len(typed))
		for index, member := range typed {
			result[index] = objectMember{key: member.key, value: coarseTimes(member.value, member.key, start)}
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = coarseTimes(item, "", start)
		}
		return result
	case json.Number:
		whole, err := typed.Int64()
		if err == nil && key != "idle_seconds" && whole > start-timestampWindowSeconds && whole < start+timestampWindowSeconds {
			return "<time>"
		}
	}
	return value
}

// reportWalkDifference fails the walk with the seed, the step, both replies and a
// command that replays the seed.
func reportWalkDifference(t *testing.T, seed int64, step, steps int, action walkAction, sides [2]*walkSide, shown [2]string) {
	t.Helper()
	request := formatJSON(shortenLongStrings(sides[0].normalizer.value(toOrdered(action.request), "")), false)
	t.Fatalf("seed %d, step %d (%s): the replies differ\nrequest: %s\nreference: %s\ntested:    %s\nreplay: go test -tags conformance ./conformance/... -run TestDifferentialWalk -seed %d -steps %d",
		seed, step, action.label, request, shown[0], shown[1], seed, max(step, steps))
}

// toOrdered turns a request template into a value the normaliser can show, with
// secrets shown by their reference value.
func toOrdered(request fields) any {
	line, err := encodeRequest(substitute(request, 0))
	if err != nil {
		return nil
	}
	decoded, _ := decodeOrdered(line)
	return decoded
}

// walkReply is one decoded reply.
type walkReply struct {
	result    any
	errorText string
}

// parseWalkReply decodes the result or error of a reply line.
func parseWalkReply(line string) walkReply {
	var envelope struct {
		Result any    `json:"result"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal([]byte(line), &envelope); err != nil {
		return walkReply{}
	}
	return walkReply{result: envelope.Result, errorText: envelope.Error}
}

// at follows a path of object keys and array indexes, or returns nil.
func (reply walkReply) at(path ...any) any {
	current := reply.result
	for _, step := range path {
		if current == nil {
			return nil
		}
		current = lookupStep(current, step)
	}
	return current
}

// text returns the string at path, or "".
func (reply walkReply) text(path ...any) string {
	value, _ := reply.at(path...).(string)
	return value
}

// number returns the integer at path, or zero.
func (reply walkReply) number(path ...any) int64 {
	value, _ := reply.at(path...).(float64)
	return int64(value)
}
