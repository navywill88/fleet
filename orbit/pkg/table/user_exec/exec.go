// build +darwin

package user_exec

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"time"

	"github.com/kolide/launcher/pkg/osquery/tables/tablehelpers"
	"github.com/osquery/osquery-go/plugin/table"
	"github.com/rs/zerolog/log"
)

// ExecOsqueryLaunchctl runs osquery under launchctl, in a user context.
func ExecOsqueryLaunchctl(ctx context.Context, timeoutSeconds int, username string, osqueryPath string, query string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
	defer cancel()

	targetUser, err := user.Lookup(username)
	if err != nil {
		return nil, fmt.Errorf("looking up username %s: %w", username, err)
	}

	cmd := exec.CommandContext(ctx,
		"launchctl",
		"asuser",
		targetUser.Uid,
		osqueryPath,
		"--config_path", "/dev/null",
		"--disable_events",
		"--disable_database",
		"--disable_audit",
		"--ephemeral",
		"-S",
		"--json",
		query,
	)

	dir, err := os.MkdirTemp("", "osq-launchctl")
	if err != nil {
		return nil, fmt.Errorf("mktemp: %w", err)
	}
	defer os.RemoveAll(dir)

	if err := os.Chmod(dir, 0o755); err != nil {
		return nil, fmt.Errorf("chmod: %w", err)
	}

	cmd.Dir = dir

	stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
	cmd.Stdout, cmd.Stderr = stdout, stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("running osquery. Got: '%s': %w", string(stderr.Bytes()), err)
	}

	return stdout.Bytes(), nil
}

func ExecOsqueryLaunchctlParsed(ctx context.Context, timeoutSeconds int, username string, osqueryPath string, query string) ([]map[string]string, error) {
	outBytes, err := ExecOsqueryLaunchctl(ctx, timeoutSeconds, username, osqueryPath, query)
	if err != nil {
		return nil, err
	}

	var osqueryResults []map[string]string

	if err := json.Unmarshal(outBytes, &osqueryResults); err != nil {
		log.Info().Err(err).Msg("error unmarshalling json")
		return nil, fmt.Errorf("unmarshalling json: %w", err)
	}

	return osqueryResults, nil
}

const (
	allowedUsernameCharacters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-. "
)

// struct Table provides a table generator that will
// call osquery in a user context.
//
// This is necessary because some macOS tables need to run in user
// context. Running this in root context returns no
// results. Furthermore, these cannot run in sudo. Sudo sets the
// effective uid, but instead we need a bunch of keychain context.
//
// Resulting data is odd. If a user is logged in, even inactive,
// correct data is returned. If a user has not ever configured these
// settings, the default values are returned. If the user has
// configured these settings, _and_ the user is not logged in, no data
// is returned.

type Table struct {
	osqueryd  string
	query     string
	tablename string
}

func TablePlugin(
	tablename string, osqueryd string, osqueryQuery string, columns []table.ColumnDefinition,
) *table.Plugin {
	columns = append(columns, table.TextColumn("user"))

	t := &Table{
		osqueryd:  osqueryd,
		query:     osqueryQuery,
		tablename: tablename,
	}

	return table.NewPlugin(t.tablename, columns, t.generate)
}

func (t *Table) generate(ctx context.Context, queryContext table.QueryContext) ([]map[string]string, error) {
	var results []map[string]string

	users := tablehelpers.GetConstraints(queryContext, "user",
		tablehelpers.WithAllowedCharacters(allowedUsernameCharacters),
	)

	if len(users) == 0 {
		return nil, fmt.Errorf("The %s table requires a user", t.tablename)
	}

	for _, user := range users {
		osqueryResults, err := ExecOsqueryLaunchctlParsed(ctx, 5, user, t.osqueryd, t.query)
		if err != nil {
			continue
		}

		for _, row := range osqueryResults {
			row["user"] = user
			results = append(results, row)
		}
	}
	return results, nil
}