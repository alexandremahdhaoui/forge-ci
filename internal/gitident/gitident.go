// Package gitident supplies the committer identity git needs to write an
// object when the host has none.
//
// It exists because the same defect shipped twice. gitadapter.Commit and
// releaseadapter.Tag both shell out to git, both write an object, and both
// let git find an identity in ambient config. A developer machine has a
// global one and a CI runner has none, so every local run passed and every
// pipeline run died on "fatal: empty ident name". Fixing the first left the
// second broken, because they share no code.
//
// Anything that writes a git object - a commit, an annotated tag, a note -
// calls Args and puts the result before the subcommand. Anything that only
// reads does not need it.
package gitident

import (
	"context"
	"strings"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/execadapter"
)

// The identity used when the host names no committer.
//
// No Signed-off-by trailer goes with it. That trailer is a DCO attestation,
// a human certifying they wrote the change and may submit it; a revision
// record or a release tag is written by the tool, and signing off on the
// tool's behalf claims something nobody said.
const (
	Name  = "Alexandre Mahdhaoui"
	Email = "alexandre.mahdhaoui@gmail.com"
)

// Args answers the -c flags to put before a git subcommand that writes an
// object. It is a floor and not an override: a host that already names a
// committer gets nothing back, so a local run stays attributed to whoever ran
// it, and only a host with none gets the identity above.
//
// Signing goes off with the fallback for the same reason the fallback is
// needed. A host with no identity configured has no signing key either, and
// an ambient commit.gpgsign or tag.gpgsign would trade one failure for
// another.
//
// One limit worth knowing: -c loses to the environment. An empty
// GIT_COMMITTER_NAME cannot be overridden this way, only an absent one. A
// runner leaves them absent, which is the case this serves.
func Args(ctx context.Context, runner execadapter.Runner, dir string) []string {
	if has(ctx, runner, dir) {
		return nil
	}

	return []string{
		"-c", "user.name=" + Name,
		"-c", "user.email=" + Email,
		"-c", "commit.gpgsign=false",
		"-c", "tag.gpgsign=false",
	}
}

// has asks git whether it can name a committer at all. git var resolves
// config, environment and defaults together, which is the same answer the
// write itself would reach, so this cannot disagree with it.
func has(ctx context.Context, runner execadapter.Runner, dir string) bool {
	res, err := runner.Run(ctx, dir, "git", "var", "GIT_COMMITTER_IDENT")

	return err == nil && res.ExitCode == 0 && strings.TrimSpace(res.Stdout) != ""
}
