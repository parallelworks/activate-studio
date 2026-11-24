package gufi

import (
	"strings"
	"testing"
)

// Each refused query must be refused whole, with a message naming the
// function or module.
func TestRawPlanRefusesFunctionsThatRunProgramsOrOpenFiles(t *testing.T) {
	cases := []struct{ name, sql string }{
		// case and quoting
		{"strop", "SELECT strop('id') FROM summary"},
		{"strop", "SELECT STROP('id') FROM summary"},
		{"strop", "SELECT Strop('id') FROM summary"},
		{"strop", `SELECT "strop"('id') FROM summary`},
		{"strop", "SELECT `strop`('id') FROM summary"},
		{"strop", "SELECT [strop]('id') FROM summary"},
		{"strop", `SELECT "STROP"('id') FROM summary`},
		{"strop", "SELECT [Strop]('id') FROM summary"},
		// comments and spacing between the name and its parenthesis
		{"strop", "SELECT strop/**/('id') FROM summary"},
		{"strop", "SELECT strop\n('id') FROM summary"},
		{"strop", "SELECT strop \t\r\n ('id') FROM summary"},
		{"strop", "SELECT strop/* a\ncomment */\n('id') FROM summary"},
		{"strop", "SELECT strop -- a comment\n('id') FROM summary"},
		{"strop", "SELECT 1 /* x */ , strop('id')"},
		// inside other SQL
		{"strop", "SELECT * FROM (WITH x AS (SELECT strop('id') AS v) SELECT v FROM x)"},
		{"strop", "WITH x AS (SELECT strop('id') AS v) SELECT v FROM x"},
		{"strop", "SELECT name FROM vrpentries WHERE size > (SELECT length(strop('id')))"},
		{"intop", "SELECT name FROM vrpentries WHERE EXISTS (SELECT 1 WHERE intop('x') = 0) AND strop('y') IS NULL"},
		{"strop", "CREATE TRIGGER t AFTER INSERT ON entries BEGIN SELECT strop('id'); END"},
		{"strop", "CREATE VIEW v AS SELECT strop('id')"},
		{"strop", "SELECT name FROM vrpentries UNION ALL SELECT strop('id')"},
		{"strop", "SELECT CASE WHEN 1 THEN strop('id') END"},
		{"strop", "SELECT name FROM vrpentries ORDER BY strop('id')"},
		// the function list
		{"intop", "SELECT intop('true')"},
		{"blobop", "SELECT blobop('true')"},
		{"lineop", "SELECT lineop('true')"},
		{"create_spread", "SELECT create_spread('a', 'b', 'c', 'd', 'e', 'f', 'g')"},
		{"spread_to_external", "SELECT spread_to_external(1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12)"},
		{"load_extension", "SELECT load_extension('x.so')"},
		{"load_extension", "SELECT LOAD_EXTENSION ('x.so', 'init')"},
		{"lembed_model_from_file", "SELECT lembed_model_from_file('/x')"},
		{"lembed_models", "SELECT * FROM lembed_models"},
		{"readfile", "SELECT readfile('/x')"},
		{"writefile", "SELECT writefile('/x', 'y')"},
		{"fsdir", "SELECT * FROM fsdir('/')"},
		{"lsdir", "SELECT * FROM lsdir('/')"},
		{"zipfile", "SELECT * FROM zipfile('/x.zip')"},
		{"sqlite_dbpage", "SELECT * FROM sqlite_dbpage"},
		{"fts3_tokenizer", "SELECT fts3_tokenizer('x', y'00')"},
		// virtual tables
		{"run_vt", "SELECT * FROM run_vt"},
		{"run_vt", "SELECT * FROM 'run_vt'"},
		{"run_vt", "CREATE VIRTUAL TABLE temp.x USING run_vt(cmd='id')"},
		{"run_vt", "CREATE VIRTUAL TABLE x USING \"run_vt\"('id')"},
		{"gufi_vt", "SELECT * FROM gufi_vt('/')"},
		{"gufi_vt_pentries", "SELECT * FROM gufi_vt_pentries('/')"},
	}
	for _, c := range cases {
		_, err := RawPlan(c.sql, "")
		if err == nil {
			t.Errorf("allowed: %s", c.sql)
			continue
		}
		if !strings.Contains(err.Error(), c.name) {
			t.Errorf("%s: the message %q does not name %s", c.sql, err.Error(), c.name)
		}
	}
}

// What is refused by statement rather than by name.
func TestRawPlanRefusesFilesAndOtherStatements(t *testing.T) {
	for _, sql := range []string{
		"ATTACH '/etc/x.db' AS x",
		"attach database '/tmp/x.db' as x",
		"SELECT 1; ATTACH '/tmp/x.db' AS x",
		"DETACH x",
		"SELECT 1; DETACH x",
		"CREATE VIRTUAL TABLE x USING fts5(a)",
		"CREATE VIRTUAL TABLE x USING vec0(a float[4])",
		"create virtual table x using fts5vocab(a, b)",
		"SELECT 1; SELECT 2",
		"PRAGMA writable_schema = 1",
		"VACUUM INTO '/tmp/x.db'",
		"SELECT 1\x00, strop('id')",
	} {
		if _, err := RawPlan(sql, ""); err == nil {
			t.Errorf("allowed: %q", sql)
		}
	}
}

func TestRawPlanAllowsOrdinaryQueries(t *testing.T) {
	for _, sql := range []string{
		"SELECT name FROM vrpentries WHERE name LIKE '%stroppy%'",
		"SELECT name FROM vrpentries WHERE name LIKE '%strop%'",
		"SELECT name, size FROM vrpentries WHERE size > 10",
		"SELECT name FROM vrpentries WHERE name = 'strop.txt'",
		"SELECT name AS stropped, size FROM vrpentries",
		"SELECT name FROM vrpentries -- strop('id')\nWHERE size > 0",
		"SELECT name FROM vrpentries /* strop('id') */ WHERE size > 0",
		"SELECT rpath(sname, sroll) || '/' || name, uidtouser(uid), human_readable_size(size) FROM vrpentries",
		"SELECT name FROM vrpentries WHERE name = 'it''s strop(''id'')'",
		"SELECT * FROM (SELECT name FROM vrpentries) WHERE name GLOB '*.pdf'",
		"SELECT e.name, b.chunk_content FROM vrpentries e JOIN ext_bm25 b ON b.inode = e.inode",
		"SELECT vec_distance_cosine(embedding, embedding) FROM ext_vectors",
	} {
		if _, err := RawPlan(sql, ""); err != nil {
			t.Errorf("%s refused: %v", sql, err)
		}
	}
}

func TestSQLNamesReadsLikeSQLite(t *testing.T) {
	got := strings.Join(sqlNames("SELECT \"a\"\"b\", `c``d`, [e f], 'g''h', 'k', X'00', -- l\n m /* n */ o"), ",")
	want := `select,a"b,c` + "`" + `d,e f,k,x,00,m,o`
	if got != want {
		t.Errorf("names %q, want %q", got, want)
	}
	// an unterminated comment or bracket hides nothing from the check
	if RefusedName("SELECT 1 /* strop(") != "" || RefusedName("SELECT [strop") != "strop" {
		t.Error("unterminated comment or bracket read wrongly")
	}
}
