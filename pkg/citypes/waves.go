package citypes

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ErrCycle marks a needs graph that cannot be ordered.
var ErrCycle = errors.New("repos depend on each other in a cycle")

// Waves splits the repos a target names into ordered groups: everything in a
// wave may run at once, and a wave starts only once the one before it is
// done.
//
// A repo declares what it needs and nothing else. Nobody writes an order, so
// adding a repo cannot silently reorder the others, and a list that happened
// to be written in a working order stops being load-bearing.
//
// Only edges between repos THIS target names count. A target that runs in one
// repo has one wave whatever the rest of the workspace declares: a
// dependency says what must be built first when both are being built, not
// that the other must be built at all.
//
// Declared order is preserved inside a wave. A wave is a set, so the order
// does not decide anything, and keeping it makes the rendered script and the
// log read in the order the operator wrote.
func Waves(in []string, needs map[string][]string) ([][]string, error) {
	// No edge applies: every dir is its own wave, in the order given. That is
	// exactly what this did before dependencies existed - one at a time, in
	// list order - so a pipeline that declares nothing is unchanged, down to
	// the bytes of the rendered script.
	if !anyEdge(in, needs) {
		waves := make([][]string, 0, len(in))
		for _, name := range in {
			waves = append(waves, []string{name})
		}

		return waves, nil
	}

	pending := make(map[string]bool, len(in))
	for _, name := range in {
		pending[name] = true
	}

	waves := [][]string{}

	for len(pending) > 0 {
		wave := []string{}

		// Ranged over the input rather than the map, so a wave carries
		// declared order and two runs of the same config render the same
		// script. Ranging a map here would shuffle it every run.
		for _, name := range in {
			if pending[name] && ready(name, needs, pending) {
				wave = append(wave, name)
			}
		}

		// Nothing is ready and something is left: every remaining repo waits
		// on another that is also waiting. Name them all, because a cycle
		// that a sort silently dropped is worse than one reported.
		if len(wave) == 0 {
			return nil, fmt.Errorf("%w: %s", ErrCycle, strings.Join(sorted(pending), ", "))
		}

		for _, name := range wave {
			delete(pending, name)
		}

		waves = append(waves, wave)
	}

	return waves, nil
}

// anyEdge answers whether the needs graph constrains anything among these
// repos. An edge to a repo the target does not name is not an edge here.
func anyEdge(in []string, needs map[string][]string) bool {
	named := make(map[string]bool, len(in))
	for _, name := range in {
		named[name] = true
	}

	for _, name := range in {
		for _, need := range needs[name] {
			if need != name && named[need] {
				return true
			}
		}
	}

	return false
}

// ready answers whether everything this repo needs has already left the
// pending set. A need on a repo the target does not name is satisfied by
// construction: it is not being built here, so there is nothing to wait for.
func ready(name string, needs map[string][]string, pending map[string]bool) bool {
	for _, need := range needs[name] {
		if need != name && pending[need] {
			return false
		}
	}

	return true
}

func sorted(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for name := range set {
		out = append(out, name)
	}

	sort.Strings(out)

	return out
}

// NeedsOf reads the needs graph off the checkouts a run was handed. The
// pipeline declares it once on repos:, and both compute engines resolve the
// same graph from the same field, so a local run and a remote one order the
// work identically.
func NeedsOf(repos []RepoCheckout) map[string][]string {
	needs := make(map[string][]string, len(repos))

	for _, repo := range repos {
		if len(repo.Needs) > 0 {
			needs[repo.Name] = repo.Needs
		}
	}

	return needs
}

// WavesFor is Waves over one target's dirs. A target that names no repo runs
// once at the root, which is one wave of one.
func WavesFor(t Target, in RunInput) ([][]string, error) {
	if len(t.In) == 0 {
		return [][]string{{""}}, nil
	}

	waves, err := Waves(t.In, NeedsOf(in.Repos))
	if err != nil {
		return nil, fmt.Errorf("ordering target %q: %w", t.Alias, err)
	}

	return waves, nil
}
