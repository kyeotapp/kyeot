package store

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// This test breaks when the schema definition changes. Bump SchemaVersion by 1 and add the new fingerprint to the table below.
var schemaFingerprints = map[int]string{
	1: "551f8744f7298a06",
	2: "ea26ec2bfb225b48", // list_groups, list_prefs
	3: "44461817d7e82e51", // server_state
	4: "950b225382ef7a29", // ui_parts
	5: "14fe106bb134cad7", // ui_layouts.off_order
	6: "72c9578ec79e38b0", // link_previews
	7: "0f1b3632b1dd7ae6", // owners.avatar
	8: "005b6359551fbfcd", // room_reports, blocks, owners.terms_*, rooms.filter_off, room_messages.removed_at
	9: "a60e6b9221913349", // list_prefs.group_pos
}

func schemaFingerprint() string {
	h := sha256.New()
	fmt.Fprintln(h, schema, uploadsTable)
	for _, c := range addColumns {
		fmt.Fprintln(h, c.table, c.column, c.def)
	}
	for _, q := range indexes {
		fmt.Fprintln(h, q)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func TestSchemaVersionFollowsDefinitions(t *testing.T) {
	got := schemaFingerprint()
	if want, ok := schemaFingerprints[SchemaVersion]; !ok || want != got {
		t.Fatalf("DB schema definition changed (fingerprint %s). Bump SchemaVersion to %d and add %d: %q to schemaFingerprints", got, SchemaVersion+1, SchemaVersion+1, got)
	}
}

// Fingerprint of the data-fix function bodies run on upgrade, measured from 9. When it changes, bump SchemaVersion by 1 and add the new fingerprint.
var dataFixFingerprints = map[int]string{
	9: "ae6316d61e0454f2",
}

var dataFixFuncs = []string{"migrate", "migrateUploadsUsed", "uniqueRuns", "scanStrings", "DefaultAvatar"}

// dataFixFingerprint is the fingerprint of a function body without comments and line breaks.
func dataFixFingerprint(t *testing.T) string {
	t.Helper()
	fset := token.NewFileSet()
	found := map[string]*ast.FuncDecl{}
	for _, name := range []string{"store.go", "uploads.go"} {
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil && slices.Contains(dataFixFuncs, fn.Name.Name) {
				found[fn.Name.Name] = fn
			}
		}
	}
	h := sha256.New()
	for _, name := range dataFixFuncs {
		fn, ok := found[name]
		if !ok {
			t.Fatalf("data-fix function %s not found. Fix dataFixFuncs", name)
		}
		printer.Fprint(h, fset, fn)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func TestSchemaVersionFollowsDataFixes(t *testing.T) {
	got := dataFixFingerprint(t)
	if want, ok := dataFixFingerprints[SchemaVersion]; !ok || want != got {
		t.Fatalf("upgrade data fixes (%v) changed (fingerprint %s). Bump SchemaVersion to %d and add %d to dataFixFingerprints and schemaFingerprints", dataFixFuncs, got, SchemaVersion+1, SchemaVersion+1)
	}
}

func TestBackupFailureLeavesDBUntouched(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kyeot.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.Exec(`CREATE TABLE owners (id TEXT PRIMARY KEY, name TEXT NOT NULL)`)
	db.Exec(`CREATE TABLE tasks (id TEXT PRIMARY KEY, owner_id TEXT NOT NULL, agent_id TEXT NOT NULL, title TEXT NOT NULL, state TEXT NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL)`)
	db.Close()
	before, _ := os.ReadFile(path)

	at := time.Date(2026, 11, 20, 1, 2, 3, 0, time.Local)
	backupClock = func() time.Time { return at }
	defer func() { backupClock = time.Now }()
	taken := path + ".bak-0-20261120-010203"
	os.WriteFile(taken, []byte("existing file"), 0o600) // a backup fails if the same name exists

	if s, err := Open(path); err == nil {
		s.Close()
		t.Fatal("opened without backup")
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Fatal("db changed after failed backup")
	}
	if got, _ := os.ReadFile(taken); string(got) != "existing file" {
		t.Fatal("existing file overwritten")
	}
}

func TestMigrationIsOneTransaction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kyeot.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.Exec(`CREATE TABLE owners (id TEXT PRIMARY KEY, name TEXT NOT NULL)`)
	db.Exec(`CREATE TABLE tasks (id TEXT PRIMARY KEY, owner_id TEXT NOT NULL, agent_id TEXT NOT NULL, title TEXT NOT NULL, state TEXT NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL)`)
	db.Close()

	saved := addColumns
	addColumns = append(slices.Clone(addColumns), struct{ table, column, def string }{"no_such_table", "x", "TEXT"})
	defer func() { addColumns = saved }()
	if s, err := Open(path); err == nil {
		s.Close()
		t.Fatal("broken migration succeeded")
	}

	db, _ = sql.Open("sqlite", path)
	defer db.Close()
	var cols, tables, ver int
	db.QueryRow(`SELECT count(*) FROM pragma_table_info('tasks')`).Scan(&cols)
	db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table'`).Scan(&tables)
	db.QueryRow(`PRAGMA user_version`).Scan(&ver)
	if cols != 7 || tables != 2 || ver != 0 {
		t.Fatalf("half migrated: tasks %d cols, %d tables, user_version %d", cols, tables, ver)
	}
}
