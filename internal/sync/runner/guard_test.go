package runner_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ADR 0002: sync writes only *_source columns. Each SQL statement in a sync
// query file that writes library_entries may not name a user-owned column, and
// none may write other user-owned tables. The trigger in runner_test.go checks
// the same rule at runtime.
func TestSyncSQLWritesOnlySourceColumns(t *testing.T) {
	files, err := filepath.Glob("../../db/queries/sync*.sql")
	if err != nil || len(files) == 0 {
		t.Fatalf("no sync query files: %v", err)
	}
	userCols := regexp.MustCompile(`\b(ownership_user|played_user|backlog|visibility|notes|tags|acquired_on|edition_id)\b`)
	writes := regexp.MustCompile(`(?i)\b(insert\s+into|update|delete\s+from)\s+(\w+)`)
	forbidden := map[string]bool{"reviews": true, "experiences": true, "progress_events": true, "lists": true, "list_items": true, "activity_events": true, "users": true}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, stmt := range strings.Split(string(b), "-- name:") {
			name, _, _ := strings.Cut(strings.TrimSpace(stmt), " ")
			body := stripComments(stmt)
			for _, m := range writes.FindAllStringSubmatch(body, -1) {
				table := strings.ToLower(m[2])
				if forbidden[table] {
					t.Errorf("%s %s writes %s", f, name, table)
				}
				// Catalog fixes touch only items sync created and skip fields an admin locked.
				if table == "media_items" && strings.HasPrefix(strings.ToLower(m[1]), "update") &&
					!strings.Contains(body, "cover_image_id is null") &&
					(!strings.Contains(body, "locked_fields") || !strings.Contains(body, "created_by_user_id is null") || !strings.Contains(body, "metadata_provider is null")) {
					t.Errorf("%s %s updates media_items without the manual-item and locked_fields guards", f, name)
				}
				if table == "library_entries" {
					if strings.Contains(strings.ToLower(m[1]), "delete") {
						t.Errorf("%s %s deletes library entries", f, name)
					}
					if c := userCols.FindString(body); c != "" {
						t.Errorf("%s %s writes library_entries and names user column %s", f, name, c)
					}
				}
			}
		}
	}
}

// Sync packages reach the database only through Sync* queries and core reads,
// never through another feature's queries.
func TestSyncCodeUsesNoFeatureQueries(t *testing.T) {
	call := regexp.MustCompile(`\.(Library|Experiences|Reviews|Lists|Journal|Accounts|Exchange|Activity)[A-Z]\w*\(`)
	err := filepath.WalkDir("..", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, m := range call.FindAllString(string(b), -1) {
			t.Errorf("%s calls %s", p, m)
		}
		if strings.Contains(string(b), "activity.Record") {
			t.Errorf("%s records activity; imported events stay out of the journal", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func stripComments(s string) string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}
